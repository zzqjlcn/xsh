package credential

import (
	"os"
	"strconv"
	"testing"
	"time"
)

func TestWindowsCredentialRoundTrip(t *testing.T) {
	if os.Getenv("XSH_INTEGRATION_TEST") == "" {
		t.Skip("set XSH_INTEGRATION_TEST=1 to test Windows Credential Manager")
	}
	t.Setenv("XSH_CONFIG_HOME", t.TempDir())
	alias := "xsh-test-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	t.Cleanup(func() { _ = DeletePassword(alias, nil) })

	if err := SetPassword(alias, "temporary-password"); err != nil {
		t.Fatal(err)
	}
	password, err := GetPassword(alias, nil)
	if err != nil {
		t.Fatal(err)
	}
	if password != "temporary-password" {
		t.Fatalf("password = %q", password)
	}
	if err := DeletePassword(alias, nil); err != nil {
		t.Fatal(err)
	}
	password, err = GetPassword(alias, nil)
	if err != nil {
		t.Fatal(err)
	}
	if password != "" {
		t.Fatal("credential still exists after deletion")
	}
}
