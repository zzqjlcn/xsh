package form

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/zzqjlcn/xsh/internal/config"
)

type Result struct {
	Host            config.SSHHost
	Save            bool
	Password        string
	PasswordChanged bool
	SavePassword    bool
}

func Edit(current *config.SSHHost) (Result, error) {
	reader := bufio.NewReader(os.Stdin)
	host := config.SSHHost{Port: 22, AuthType: config.AuthAuto}
	if current != nil {
		host = *current
		host.Tags = append([]string(nil), current.Tags...)
	}

	fmt.Println("\nSSH Host")
	fmt.Println(strings.Repeat("─", 48))
	host.Name = prompt(reader, "Name", host.Name)
	host.Hostname = prompt(reader, "Hostname/IP", host.Hostname)
	host.User = prompt(reader, "Username", host.User)

	portText := prompt(reader, "Port", strconv.Itoa(host.Port))
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return Result{}, fmt.Errorf("invalid port %q", portText)
	}
	host.Port = port

	tags := prompt(reader, "Tags (comma separated)", strings.Join(host.Tags, ","))
	host.Tags = splitTags(tags)
	auth := strings.ToLower(prompt(reader, "Auth (auto/password/private_key)", string(host.AuthType)))
	host.AuthType = config.AuthType(auth)
	host.IdentityFile = prompt(reader, "Identity File", host.IdentityFile)
	host.ProxyJump = prompt(reader, "ProxyJump alias", host.ProxyJump)

	if err := host.Validate(); err != nil {
		return Result{}, err
	}

	result := Result{Host: host, SavePassword: host.SavePassword}
	if host.AuthType != config.AuthPrivateKey {
		label := "Password [blank = none]: "
		if current != nil && current.SavePassword {
			label = "Password [blank = keep, '-' = remove]: "
		}
		password, err := ReadPassword(label)
		if err != nil {
			return Result{}, err
		}
		switch password {
		case "":
		case "-":
			result.PasswordChanged = true
			result.SavePassword = false
			result.Host.SavePassword = false
		default:
			result.PasswordChanged = true
			result.Password = password
			result.SavePassword = confirm(reader, "Save password securely?", true)
			result.Host.SavePassword = result.SavePassword
		}
	}

	fmt.Printf("\nSave %s (%s@%s)? [Y/n]: ", host.Name, host.User, host.Hostname)
	answer, _ := reader.ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	result.Save = answer != "n" && answer != "no"
	return result, nil
}

func ReadPassword(label string) (string, error) {
	fmt.Print(label)
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Println()
	if err != nil {
		return "", err
	}
	return string(value), nil
}

func Confirm(label string, defaultYes bool) bool {
	return confirm(bufio.NewReader(os.Stdin), label, defaultYes)
}

func prompt(reader *bufio.Reader, label, current string) string {
	if current == "" {
		fmt.Printf("%-28s: ", label)
	} else {
		fmt.Printf("%-28s [%s]: ", label, current)
	}
	value, _ := reader.ReadString('\n')
	value = strings.TrimSpace(value)
	if value == "" {
		return current
	}
	return value
}

func confirm(reader *bufio.Reader, label string, defaultYes bool) bool {
	suffix := " [y/N]: "
	if defaultYes {
		suffix = " [Y/n]: "
	}
	fmt.Print(label + suffix)
	value, _ := reader.ReadString('\n')
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return defaultYes
	}
	return value == "y" || value == "yes"
}

func splitTags(value string) []string {
	seen := make(map[string]bool)
	var tags []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" && !seen[item] {
			seen[item] = true
			tags = append(tags, item)
		}
	}
	return tags
}
