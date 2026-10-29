package webstore

import (
	"database/sql"
	"errors"
	"github.com/zzqjlcn/xsh/internal/database"
)

const DefaultSessionHours = 12

func ValidSessionHours(hours int) bool {
	switch hours {
	case 1, 4, 8, 12, 24, 72, 168, 720:
		return true
	}
	return false
}

func SessionHours(userID int64) (int, error) {
	db, err := database.Open()
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var hours int
	err = db.QueryRow(`SELECT session_hours FROM web_user_security WHERE user_id=?`, userID).Scan(&hours)
	if errors.Is(err, sql.ErrNoRows) {
		return DefaultSessionHours, nil
	}
	if err != nil {
		return 0, err
	}
	if !ValidSessionHours(hours) {
		return DefaultSessionHours, nil
	}
	return hours, nil
}

func SetSessionHours(userID int64, hours int) error {
	if !ValidSessionHours(hours) {
		return errors.New("不支持的登录有效期")
	}
	db, err := database.Open()
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO web_user_security(user_id,session_hours) VALUES(?,?) ON CONFLICT(user_id) DO UPDATE SET session_hours=excluded.session_hours`, userID, hours)
	return err
}
