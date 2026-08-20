package filecopy

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
)

func TestCopyFileLocalToLocal(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.txt")
	destination := filepath.Join(root, "destination.txt")
	if err := os.WriteFile(source, []byte("hello xsh"), 0o640); err != nil {
		t.Fatal(err)
	}
	var latest Progress
	copier := Copier{Source: LocalFileSystem(), Destination: LocalFileSystem()}
	if err := copier.Copy(context.Background(), source, destination, func(progress Progress) { latest = progress }); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello xsh" {
		t.Fatalf("destination = %q", data)
	}
	if latest.Phase != "Complete" || latest.BytesCopied != int64(len(data)) || latest.FilesCopied != 1 {
		t.Fatalf("progress = %#v", latest)
	}
}

func TestCopyBetweenLocalAndSFTP(t *testing.T) {
	client := newInMemorySFTP(t)
	root := t.TempDir()
	localSource := filepath.Join(root, "upload.txt")
	if err := os.WriteFile(localSource, []byte("through sftp"), 0o600); err != nil {
		t.Fatal(err)
	}

	upload := Copier{Source: LocalFileSystem(), Destination: RemoteFileSystem("dev", client)}
	if err := upload.Copy(context.Background(), localSource, "/upload.txt", nil); err != nil {
		t.Fatal(err)
	}
	remoteFile, err := client.Open("/upload.txt")
	if err != nil {
		t.Fatal(err)
	}
	remoteData, err := io.ReadAll(remoteFile)
	_ = remoteFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(remoteData) != "through sftp" {
		t.Fatalf("remote data = %q", remoteData)
	}

	localDestination := filepath.Join(root, "download.txt")
	download := Copier{Source: RemoteFileSystem("dev", client), Destination: LocalFileSystem()}
	if err := download.Copy(context.Background(), "/upload.txt", localDestination, nil); err != nil {
		t.Fatal(err)
	}
	localData, err := os.ReadFile(localDestination)
	if err != nil {
		t.Fatal(err)
	}
	if string(localData) != "through sftp" {
		t.Fatalf("local data = %q", localData)
	}
}

func TestCopyDirectoryLocalToLocal(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "file.txt"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	copier := Copier{Source: LocalFileSystem(), Destination: LocalFileSystem()}
	if err := copier.Copy(context.Background(), source, destination, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(destination, "nested", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "content" {
		t.Fatalf("copied data = %q", data)
	}
}

func TestCopyRejectsDestinationInsideSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	copier := Copier{Source: LocalFileSystem(), Destination: LocalFileSystem()}
	err := copier.Copy(context.Background(), source, filepath.Join(source, "nested"), nil)
	if err == nil || !strings.Contains(err.Error(), "inside") {
		t.Fatalf("Copy() error = %v", err)
	}
}

func TestCompleteLocalPath(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "alpha-dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "alpha.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	candidates, err := LocalFileSystem().Complete(context.Background(), filepath.Join(root, "alp"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(root, "alpha-dir") + string(filepath.Separator), filepath.Join(root, "alpha.txt")}
	if len(candidates) != len(want) || candidates[0] != want[0] || candidates[1] != want[1] {
		t.Fatalf("Complete() = %#v, want %#v", candidates, want)
	}
}

func TestCompleteRemotePath(t *testing.T) {
	client := newInMemorySFTP(t)
	if err := client.Mkdir("/alpha-dir"); err != nil {
		t.Fatal(err)
	}
	file, err := client.Create("/alpha.txt")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	candidates, err := RemoteFileSystem("dev", client).Complete(context.Background(), "/alp")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/alpha-dir/", "/alpha.txt"}
	if len(candidates) != len(want) || candidates[0] != want[0] || candidates[1] != want[1] {
		t.Fatalf("Complete() = %#v, want %#v", candidates, want)
	}
}

func newInMemorySFTP(t *testing.T) *sftp.Client {
	t.Helper()
	serverConnection, clientConnection := net.Pipe()
	server := sftp.NewRequestServer(serverConnection, sftp.InMemHandler())
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve() }()
	client, err := sftp.NewClientPipe(clientConnection, clientConnection)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		_ = serverConnection.Close()
		_ = clientConnection.Close()
		<-serverDone
	})
	return client
}
