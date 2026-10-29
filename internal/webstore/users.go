package webstore

import (
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zzqjlcn/xsh/internal/database"
)

var ErrLogin = errors.New("用户名或密码错误")
var ErrNotFound = errors.New("记录不存在")
var ErrConflict = errors.New("记录已存在")
var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,64}$`)

type User struct {
	authVersion []byte
	ID          int64  `json:"id"`
	Username    string `json:"username"`
	Admin       bool   `json:"admin"`
	Disabled    bool   `json:"disabled"`
}

func HasUsers() (bool, error) {
	db, err := database.Open()
	if err != nil {
		return false, err
	}
	defer db.Close()
	var count int
	err = db.QueryRow(`SELECT count(*) FROM web_users`).Scan(&count)
	return count > 0, err
}

// SetupCode is only needed before the first administrator is created. It is
// printed by xsh web, never returned through unauthenticated HTTP endpoints.
func SetupCode() (string, error) {
	dir, err := database.Dir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "web-setup-code")
	if data, err := os.ReadFile(path); err == nil {
		code := strings.TrimSpace(string(data))
		decoded, decodeErr := hex.DecodeString(code)
		if decodeErr != nil || len(decoded) != 24 {
			return "", errors.New("初始化码文件无效，请检查 web-setup-code")
		}
		return code, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	data, err := randomBytes(24)
	if err != nil {
		return "", err
	}
	code := hex.EncodeToString(data)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		return SetupCode()
	}
	if err != nil {
		return "", err
	}
	_, writeErr := f.WriteString(code)
	closeErr := f.Close()
	return code, errors.Join(writeErr, closeErr)
}

func ValidatePassword(password string) error {
	if len([]rune(password)) < 8 || len(password) > 1024 {
		return errors.New("登录密码至少 8 个字符，且不超过 1024 字节")
	}
	return nil
}

func CreateUser(username, password string, first bool, initial []InitialHost) (User, []byte, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) {
		return User{}, nil, errors.New("用户名需为 3–64 位字母、数字、点、下划线或短横线")
	}
	if err := ValidatePassword(password); err != nil {
		return User{}, nil, err
	}
	salt, err := randomBytes(16)
	if err != nil {
		return User{}, nil, err
	}
	key := derive(password, salt)
	defer clear(key)
	dataKey, err := randomBytes(32)
	if err != nil {
		return User{}, nil, err
	}
	success := false
	defer func() {
		if !success {
			clear(dataKey)
		}
	}()
	wrapped, err := seal(key, dataKey, "xsh-web-user-key:"+username)
	if err != nil {
		return User{}, nil, err
	}
	db, err := database.Open()
	if err != nil {
		return User{}, nil, err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return User{}, nil, err
	}
	defer tx.Rollback()
	// Taking the SQLite write lock makes first-admin creation atomic.
	if _, err := tx.Exec(`UPDATE web_users SET admin=admin WHERE 0`); err != nil {
		return User{}, nil, err
	}
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM web_users`).Scan(&count); err != nil {
		return User{}, nil, err
	}
	if first && count != 0 {
		return User{}, nil, errors.New("管理员已创建，请登录")
	}
	if !first && count == 0 {
		return User{}, nil, errors.New("请先创建管理员")
	}
	var exists int
	if err := tx.QueryRow(`SELECT count(*) FROM web_users WHERE username=?`, username).Scan(&exists); err != nil {
		return User{}, nil, err
	}
	if exists != 0 {
		return User{}, nil, ErrConflict
	}
	result, err := tx.Exec(`INSERT INTO web_users(username,admin,salt,verifier,wrapped_key) VALUES(?,?,?,?,?)`, username, first, salt, verifier(key), wrapped)
	if err != nil {
		return User{}, nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return User{}, nil, err
	}
	user := User{ID: id, Username: username, Admin: first, authVersion: verifier(key)}
	for _, item := range initial {
		if err := saveHost(tx, user.ID, dataKey, item.Host, item.Secret, false, 0); err != nil {
			return User{}, nil, fmt.Errorf("导入主机 %s: %w", item.Host.Name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return User{}, nil, err
	}
	success = true
	return user, dataKey, nil
}

func Authenticate(username, password string) (User, []byte, error) {
	if len(password) > 1024 {
		return User{}, nil, ErrLogin
	}
	username = strings.ToLower(strings.TrimSpace(username))
	db, err := database.Open()
	if err != nil {
		return User{}, nil, err
	}
	defer db.Close()
	var user User
	var salt, stored, wrapped []byte
	err = db.QueryRow(`SELECT id,username,admin,disabled,salt,verifier,wrapped_key FROM web_users WHERE username=?`, username).Scan(&user.ID, &user.Username, &user.Admin, &user.Disabled, &salt, &stored, &wrapped)
	if errors.Is(err, sql.ErrNoRows) {
		dummy := derive(password, make([]byte, 16))
		clear(dummy)
		return User{}, nil, ErrLogin
	}
	if err != nil {
		return User{}, nil, err
	}
	key := derive(password, salt)
	defer clear(key)
	if subtle.ConstantTimeCompare(verifier(key), stored) != 1 || user.Disabled {
		return User{}, nil, ErrLogin
	}
	user.authVersion = stored
	dataKey, err := unseal(key, wrapped, "xsh-web-user-key:"+user.Username)
	if err != nil {
		return User{}, nil, ErrLogin
	}
	return user, dataKey, nil
}

func SessionValid(user User) bool {
	db, err := database.Open()
	if err != nil {
		return false
	}
	defer db.Close()
	var disabled bool
	var version []byte
	if db.QueryRow(`SELECT disabled,verifier FROM web_users WHERE id=?`, user.ID).Scan(&disabled, &version) != nil {
		return false
	}
	return !disabled && len(user.authVersion) > 0 && subtle.ConstantTimeCompare(version, user.authVersion) == 1
}

func Active(id int64) bool {
	db, err := database.Open()
	if err != nil {
		return false
	}
	defer db.Close()
	var disabled bool
	return db.QueryRow(`SELECT disabled FROM web_users WHERE id=?`, id).Scan(&disabled) == nil && !disabled
}
func Users() ([]User, error) {
	db, err := database.Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id,username,admin,disabled FROM web_users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Admin, &u.Disabled); err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}
func SetDisabled(id int64, disabled bool) error {
	db, err := database.Open()
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.Exec(`UPDATE web_users SET disabled=? WHERE id=? AND admin=0`, disabled, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrNotFound
	}
	return err
}

func DeleteUser(id int64) error {
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
	result, err := tx.Exec(`DELETE FROM web_users WHERE id=? AND admin=0`, id)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM web_hosts WHERE user_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM web_user_security WHERE user_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM web_shares WHERE sender_id=? OR recipient_id=?`, id, id); err != nil {
		return err
	}
	return tx.Commit()
}

func ChangeLoginPassword(user User, current, next string, dataKey []byte) error {
	if err := ValidatePassword(next); err != nil {
		return err
	}
	verified, key, err := Authenticate(user.Username, current)
	if err != nil {
		return ErrLogin
	}
	defer clear(key)
	if verified.ID != user.ID || subtle.ConstantTimeCompare(key, dataKey) != 1 {
		return ErrLogin
	}
	salt, err := randomBytes(16)
	if err != nil {
		return err
	}
	derived := derive(next, salt)
	defer clear(derived)
	wrapped, err := seal(derived, dataKey, "xsh-web-user-key:"+user.Username)
	if err != nil {
		return err
	}
	db, err := database.Open()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`UPDATE web_users SET salt=?,verifier=?,wrapped_key=? WHERE id=?`, salt, verifier(derived), wrapped, user.ID)
	return err
}
