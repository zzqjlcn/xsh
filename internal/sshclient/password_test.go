package sshclient

import (
	"bytes"
	"strings"
	"testing"
)

func TestPasswordConversation(t *testing.T) {
	for _, test := range []struct {
		name      string
		prompts   []string
		want      string
		wantError bool
	}{
		{"user", []string{"Changing password.\r\n(cur", "rent) UNIX password: ", "New pass", "word: ", "Retype new password: "}, "old-secret\nnew-secret\nnew-secret\n", false},
		{"root", []string{"New password:", "Retype new password:"}, "new-secret\nnew-secret\n", false},
		{"rejected", []string{"New password:", "BAD PASSWORD\r\nNew password:"}, "new-secret\n", true},
		{"unknown", []string{"OTP code:"}, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var input bytes.Buffer
			p := passwordConversation{input: &input, current: "old-secret", next: "new-secret"}
			var err error
			for _, prompt := range test.prompts {
				_, err = p.Write([]byte(prompt))
				if err != nil {
					break
				}
			}
			if (err != nil) != test.wantError || input.String() != test.want {
				t.Fatalf("error=%v sent=%q", err, input.String())
			}
		})
	}
}

func TestPasswordControlCharacters(t *testing.T) {
	for _, password := range []string{"a\nb", "a\rb", "a\x00b", "a\x7fb", "", strings.Repeat("a", 1025)} {
		if validPassword(password) {
			t.Fatal("unsafe password accepted")
		}
	}
	if !validPassword("spaces and $'quotes\"! are safe") {
		t.Fatal("valid password rejected")
	}
}
