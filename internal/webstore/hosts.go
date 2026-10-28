package webstore

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zzqjlcn/xsh/internal/config"
	"github.com/zzqjlcn/xsh/internal/database"
	"golang.org/x/crypto/ssh"
)

type Host struct {
	config.SSHHost
	HasPrivateKey bool `json:"has_private_key,omitempty"`
}
type Secret struct {
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
}
type InitialHost struct {
	Host   config.SSHHost
	Secret Secret
}

func hostAAD(userID int64, name string) string {
	return fmt.Sprintf("xsh-web-host:%d:%s", userID, strings.ToLower(name))
}
func ListHosts(userID int64) ([]Host, error) {
	db, err := database.Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT data FROM web_hosts WHERE user_id=? ORDER BY rowid`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hosts := []Host{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var host Host
		if err := json.Unmarshal([]byte(raw), &host); err != nil {
			return nil, err
		}
		hosts = append(hosts, host)
	}
	return hosts, rows.Err()
}
func FindHost(userID int64, name string) (Host, error) {
	db, err := database.Open()
	if err != nil {
		return Host{}, err
	}
	defer db.Close()
	var raw string
	err = db.QueryRow(`SELECT data FROM web_hosts WHERE user_id=? AND name=?`, userID, name).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return Host{}, ErrNotFound
	}
	if err != nil {
		return Host{}, err
	}
	var host Host
	err = json.Unmarshal([]byte(raw), &host)
	return host, err
}
func ReadSecret(userID int64, key []byte, name string) (Secret, error) {
	db, err := database.Open()
	if err != nil {
		return Secret{}, err
	}
	defer db.Close()
	var encrypted []byte
	if err := db.QueryRow(`SELECT secret FROM web_hosts WHERE user_id=? AND name=?`, userID, name).Scan(&encrypted); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Secret{}, ErrNotFound
		}
		return Secret{}, err
	}
	return decodeSecret(userID, key, name, encrypted)
}
func decodeSecret(userID int64, key []byte, name string, encrypted []byte) (Secret, error) {
	data, err := unseal(key, encrypted, hostAAD(userID, name))
	if err != nil {
		return Secret{}, err
	}
	defer clear(data)
	var secret Secret
	err = json.Unmarshal(data, &secret)
	return secret, err
}

func SaveHost(userID int64, key []byte, host config.SSHHost, secret Secret, update bool, shareID int64) error {
	db, err := database.Open()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := saveHost(tx, userID, key, host, secret, update, shareID); err != nil {
		return err
	}
	return tx.Commit()
}
func saveHost(tx *sql.Tx, userID int64, key []byte, host config.SSHHost, input Secret, update bool, shareID int64) error {
	host.Name = strings.TrimSpace(host.Name)
	host.Hostname = strings.TrimSpace(host.Hostname)
	host.User = strings.TrimSpace(host.User)
	host.IdentityFile = ""
	if host.AuthType == "" {
		host.AuthType = config.AuthAuto
	}
	if host.Port == 0 {
		host.Port = 22
	}
	// Private key material is account-owned encrypted data, never a server path.
	validation := host
	if validation.AuthType == config.AuthPrivateKey {
		validation.AuthType = config.AuthAuto
	}
	if err := validation.Validate(); err != nil {
		return err
	}
	var oldRaw string
	var oldCipher []byte
	var old Host
	err := tx.QueryRow(`SELECT data,secret FROM web_hosts WHERE user_id=? AND name=?`, userID, host.Name).Scan(&oldRaw, &oldCipher)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if update && !exists {
		return ErrNotFound
	}
	if !update && exists {
		return ErrConflict
	}
	secret := Secret{}
	if exists {
		if err := json.Unmarshal([]byte(oldRaw), &old); err != nil {
			return err
		}
		secret, err = decodeSecret(userID, key, old.Name, oldCipher)
		if err != nil {
			return err
		}
		host.Name = old.Name
		host.LastLogin = old.LastLogin
	} else {
		host.LastLogin = 0
	}
	if input.Password != "" {
		secret.Password = input.Password
	}
	if input.PrivateKey != "" {
		secret.PrivateKey = input.PrivateKey
	}
	if host.AuthType != config.AuthPassword {
		secret.Password = ""
	}
	if host.AuthType != config.AuthPrivateKey {
		secret.PrivateKey = ""
	}
	if host.AuthType == config.AuthPrivateKey {
		if len(secret.PrivateKey) > 64<<10 {
			return errors.New("私钥文件不能超过 64 KB")
		}
		if secret.PrivateKey == "" {
			return errors.New("请输入私钥内容")
		}
		if _, err := ssh.ParsePrivateKey([]byte(secret.PrivateKey)); err != nil {
			return errors.New("私钥格式无效，暂不支持带口令的私钥")
		}
	}
	host.SavePassword = secret.Password != ""
	if shareID != 0 {
		var recipient int64
		if err := tx.QueryRow(`SELECT recipient_id FROM web_shares WHERE id=?`, shareID).Scan(&recipient); err != nil || recipient != userID {
			return ErrNotFound
		}
	}
	data, err := json.Marshal(Host{host, secret.PrivateKey != ""})
	if err != nil {
		return err
	}
	raw, err := json.Marshal(secret)
	if err != nil {
		return err
	}
	defer clear(raw)
	encrypted, err := seal(key, raw, hostAAD(userID, host.Name))
	if err != nil {
		return err
	}
	if update {
		_, err = tx.Exec(`UPDATE web_hosts SET data=?,secret=? WHERE user_id=? AND name=?`, string(data), encrypted, userID, host.Name)
	} else {
		_, err = tx.Exec(`INSERT INTO web_hosts(user_id,name,data,secret) VALUES(?,?,?,?)`, userID, host.Name, string(data), encrypted)
	}
	if err != nil {
		return err
	}
	if shareID != 0 {
		_, err = tx.Exec(`DELETE FROM web_shares WHERE id=? AND recipient_id=?`, shareID, userID)
	}
	return err
}

func DeleteHost(userID int64, name string) error {
	db, err := database.Open()
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.Exec(`DELETE FROM web_hosts WHERE user_id=? AND name=?`, userID, name)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrNotFound
	}
	return err
}
func RecordLogin(userID int64, name string) error {
	db, err := database.Open()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err := tx.QueryRow(`SELECT data FROM web_hosts WHERE user_id=? AND name=?`, userID, name).Scan(&raw); err != nil {
		return err
	}
	var host Host
	if err := json.Unmarshal([]byte(raw), &host); err != nil {
		return err
	}
	host.LastLogin = time.Now().Unix()
	data, err := json.Marshal(host)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE web_hosts SET data=? WHERE user_id=? AND name=?`, string(data), userID, name); err != nil {
		return err
	}
	return tx.Commit()
}
func UpdatePassword(userID int64, key []byte, expected config.SSHHost, password string) error {
	db, err := database.Open()
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	var encrypted []byte
	if err := tx.QueryRow(`SELECT data,secret FROM web_hosts WHERE user_id=? AND name=?`, userID, expected.Name).Scan(&raw, &encrypted); err != nil {
		return err
	}
	var host Host
	if err := json.Unmarshal([]byte(raw), &host); err != nil {
		return err
	}
	if host.Hostname != expected.Hostname || host.User != expected.User || host.Port != expected.Port || !host.SavePassword {
		return ErrNotFound
	}
	secret, err := decodeSecret(userID, key, host.Name, encrypted)
	if err != nil {
		return err
	}
	secret.Password = password
	data, err := json.Marshal(secret)
	if err != nil {
		return err
	}
	defer clear(data)
	encrypted, err = seal(key, data, hostAAD(userID, host.Name))
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE web_hosts SET secret=? WHERE user_id=? AND name=?`, encrypted, userID, host.Name); err != nil {
		return err
	}
	return tx.Commit()
}
