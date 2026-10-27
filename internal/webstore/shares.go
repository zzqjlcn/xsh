package webstore

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/zzqjlcn/xsh/internal/database"
)

// This allowlist deliberately excludes usernames, authentication settings,
// passwords, keys/paths, jump aliases, saved flags and connection history.
type SharedConfig struct {
	Name     string   `json:"name"`
	Hostname string   `json:"hostname"`
	Port     int      `json:"port"`
	Tags     []string `json:"tags,omitempty"`
}
type Share struct {
	ID        int64        `json:"id"`
	Sender    string       `json:"sender"`
	Config    SharedConfig `json:"config"`
	CreatedAt int64        `json:"created_at"`
}

type ShareRecipient struct {
	Username string `json:"username"`
}

func ShareRecipients(senderID int64) ([]ShareRecipient, error) {
	db, err := database.Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT username FROM web_users WHERE id<>? AND disabled=0 ORDER BY username`, senderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	recipients := []ShareRecipient{}
	for rows.Next() {
		var recipient ShareRecipient
		if err := rows.Scan(&recipient.Username); err != nil {
			return nil, err
		}
		recipients = append(recipients, recipient)
	}
	return recipients, rows.Err()
}

func ShareHost(senderID int64, name string, recipients []string) error {
	if len(recipients) == 0 || len(recipients) > 100 {
		return errors.New("请选择 1–100 个接收账号")
	}
	host, err := FindHost(senderID, name)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(SharedConfig{host.Name, host.Hostname, host.Port, host.Tags})
	if err != nil {
		return err
	}
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
	// Serialize inbox-limit checks and commit the entire selection together.
	if _, err := tx.Exec(`UPDATE web_shares SET id=id WHERE 0`); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, recipient := range recipients {
		recipient = strings.ToLower(strings.TrimSpace(recipient))
		if seen[recipient] {
			continue
		}
		seen[recipient] = true
		var recipientID int64
		if err := tx.QueryRow(`SELECT id FROM web_users WHERE username=? AND disabled=0`, recipient).Scan(&recipientID); err != nil {
			return errors.New("接收账号不存在或已停用，请刷新后重选")
		}
		if recipientID == senderID {
			return errors.New("不能分享给自己")
		}
		var count int
		if err := tx.QueryRow(`SELECT count(*) FROM web_shares WHERE recipient_id=?`, recipientID).Scan(&count); err != nil {
			return err
		}
		if count >= 100 {
			return errors.New("接收者收件箱已满")
		}
		if _, err := tx.Exec(`INSERT INTO web_shares(sender_id,recipient_id,data,created_at) VALUES(?,?,?,?)`, senderID, recipientID, string(payload), time.Now().Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func Shares(userID int64) ([]Share, error) {
	db, err := database.Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT s.id,u.username,s.data,s.created_at FROM web_shares s JOIN web_users u ON u.id=s.sender_id WHERE s.recipient_id=? ORDER BY s.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	shares := []Share{}
	for rows.Next() {
		var s Share
		var raw string
		if err := rows.Scan(&s.ID, &s.Sender, &raw, &s.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &s.Config); err != nil {
			return nil, err
		}
		shares = append(shares, s)
	}
	return shares, rows.Err()
}
func DeleteShare(userID, id int64) error {
	db, err := database.Open()
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.Exec(`DELETE FROM web_shares WHERE id=? AND recipient_id=?`, id, userID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return ErrNotFound
	}
	return err
}
