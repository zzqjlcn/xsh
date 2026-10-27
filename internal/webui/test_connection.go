package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/webstore"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type testHostTrust struct {
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
}

func (e *testHostTrust) Error() string { return "需要确认服务器指纹" }

// Testing trusts unknown keys only for this request and never changes known_hosts.
func testHostKeyCallback(userID int64, trusted map[string]string) (ssh.HostKeyCallback, error) {
	base, err := config.Dir()
	if err != nil {
		return nil, err
	}
	var known ssh.HostKeyCallback
	path := filepath.Join(base, "web-known-hosts", strconv.FormatInt(userID, 10))
	if _, err := os.Stat(path); err == nil {
		known, err = knownhosts.New(path)
		if err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return func(address string, remote net.Addr, key ssh.PublicKey) error {
		if known != nil {
			err := known(address, remote, key)
			if err == nil {
				return nil
			}
			var keyErr *knownhosts.KeyError
			if !errors.As(err, &keyErr) {
				return err
			}
			for _, want := range keyErr.Want {
				if want.Key.Type() == key.Type() {
					return errors.New("服务器指纹已变化，请核实后重新连接")
				}
			}
		}
		fingerprint := ssh.FingerprintSHA256(key)
		if trusted[address] == fingerprint {
			return nil
		}
		return &testHostTrust{Address: address, Fingerprint: fingerprint}
	}, nil
}

// The deadline covers TCP, SSH handshakes, and forwarding through the jump host.
func testSSHLogin(ctx context.Context, host config.SSHHost, auth []ssh.AuthMethod, callback ssh.HostKeyCallback, jump *ssh.Client) (*ssh.Client, error) {
	address := net.JoinHostPort(host.Hostname, strconv.Itoa(host.Port))
	var connection net.Conn
	var err error
	if jump == nil {
		connection, err = (&net.Dialer{}).DialContext(ctx, "tcp", address)
	} else {
		connection, err = jump.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	client, chans, requests, err := ssh.NewClientConn(connection, address, &ssh.ClientConfig{User: host.User, Auth: auth, HostKeyCallback: callback})
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	_ = connection.SetDeadline(time.Time{})
	return ssh.NewClient(client, chans, requests), nil
}

func (s *server) testConnection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	actor := requestSession(r)
	if actor == nil {
		http.Error(w, "authentication required", 401)
		return
	}
	var input struct {
		Original string            `json:"original"`
		Host     config.SSHHost    `json:"host"`
		Password string            `json:"password"`
		Trust    map[string]string `json:"trust"`
	}
	if !readJSON(w, r, &input) {
		return
	}
	host := input.Host
	host.AuthType = config.AuthPassword
	host.IdentityFile = ""
	if host.Name == "" {
		host.Name = "connection-test"
	}
	if err := host.Validate(); err != nil {
		http.Error(w, "请填写有效的主机地址、端口和用户名", 400)
		return
	}
	password := input.Password
	if input.Original != "" {
		if _, err := webstore.FindHost(actor.User.ID, input.Original); err != nil {
			http.Error(w, "主机不存在", 404)
			return
		}
		if password == "" {
			secret, err := webstore.ReadSecret(actor.User.ID, actor.Key, input.Original)
			if err != nil {
				http.Error(w, "无法读取已保存密码", 400)
				return
			}
			password = secret.Password
		}
	}
	if password == "" {
		http.Error(w, "请填写主机密码", 400)
		return
	}
	callback, err := testHostKeyCallback(actor.User.ID, input.Trust)
	if err != nil {
		http.Error(w, "无法读取服务器指纹", 500)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	stop := context.AfterFunc(actor.Context, cancel)
	defer stop()
	var jump *ssh.Client
	if host.ProxyJump != "" {
		jumpHost, findErr := webstore.FindHost(actor.User.ID, host.ProxyJump)
		if findErr != nil {
			http.Error(w, "跳板主机不存在", 400)
			return
		}
		secret, readErr := webstore.ReadSecret(actor.User.ID, actor.Key, jumpHost.Name)
		if readErr != nil {
			http.Error(w, "无法读取跳板凭据", 400)
			return
		}
		var auth []ssh.AuthMethod
		if jumpHost.AuthType != config.AuthPrivateKey && secret.Password != "" {
			auth = append(auth, ssh.Password(secret.Password))
		}
		if jumpHost.AuthType != config.AuthPassword && secret.PrivateKey != "" {
			signer, parseErr := ssh.ParsePrivateKey([]byte(secret.PrivateKey))
			if parseErr != nil {
				http.Error(w, "跳板私钥无效", 400)
				return
			}
			auth = append(auth, ssh.PublicKeys(signer))
		}
		jump, err = testSSHLogin(ctx, jumpHost.SSHHost, auth, callback, nil)
		if err == nil {
			defer jump.Close()
			stopJump := context.AfterFunc(ctx, func() { _ = jump.Close() })
			defer stopJump()
		}
	}
	if err == nil {
		var client *ssh.Client
		client, err = testSSHLogin(ctx, host, []ssh.AuthMethod{ssh.Password(password)}, callback, jump)
		if err == nil {
			_ = client.Close()
		}
	}
	if err != nil {
		var trust *testHostTrust
		if errors.As(err, &trust) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(409)
			_ = json.NewEncoder(w).Encode(trust)
			return
		}
		message := "连接失败，请检查地址、端口和跳板配置"
		if ctx.Err() != nil {
			message = "连接超时或已取消，请检查网络和服务器状态"
		} else if strings.Contains(err.Error(), "unable to authenticate") {
			message = "密码认证失败，请检查用户名和密码"
		} else if strings.Contains(err.Error(), "服务器指纹已变化") {
			message = "服务器指纹已变化，请核实后重新连接"
		}
		http.Error(w, message, 400)
		return
	}
	w.WriteHeader(204)
}
