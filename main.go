package main

import (
	"bufio"
	"encoding/binary"
	_ "errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	_ "strings"
	"time"

	"github.com/schollz/progressbar/v3"
)

const (
	defaultPort   = "9090"
	readTimeout   = 0
	writeTimeout  = 0
	maxNameLength = 32 * 1024
)

func main() {
	if len(os.Args) < 2 {
		usage()
		return
	}
	switch os.Args[1] {
	case "server":
		runServerCmd(os.Args[2:])
	case "send":
		runSendCmd(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Println(`Файлообмен по TCP (с поддержкой каталогов и прогресса)

Использование:
  fileshare server [-addr 0.0.0.0:9090] [-out ./received]
  fileshare send   -to 192.168.1.50:9090 file_or_dir1 [file2 ...]

Пример:
  fileshare server -out ./inbox
  fileshare send -to 192.168.0.23:9090 ./docs ./photo.jpg
`)
}

type fileEntry struct {
	AbsPath string
	RelPath string
	Size    int64
}

func collectFiles(paths []string) ([]fileEntry, error) {
	var out []fileEntry
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if st.IsDir() {
			root := filepath.Clean(p)
			err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if !info.Mode().IsRegular() {
					return nil
				}
				rel, _ := filepath.Rel(filepath.Dir(root), path)
				out = append(out, fileEntry{
					AbsPath: path,
					RelPath: rel,
					Size:    info.Size(),
				})
				return nil
			})
			if err != nil {
				return nil, err
			}
		} else {
			out = append(out, fileEntry{
				AbsPath: p,
				RelPath: filepath.Base(p),
				Size:    st.Size(),
			})
		}
	}
	return out, nil
}

func runSendCmd(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	to := fs.String("to", "", "адрес получателя IP:порт")
	_ = fs.Parse(args)
	if *to == "" {
		fmt.Println("Нужно указать -to")
		os.Exit(1)
	}

	files, err := collectFiles(fs.Args())
	if err != nil {
		fmt.Println("Ошибка обхода:", err)
		os.Exit(1)
	}
	if len(files) == 0 {
		fmt.Println("Нет файлов для отправки")
		return
	}

	conn, err := net.Dial("tcp", normalizeAddr(*to))
	if err != nil {
		fmt.Println("Ошибка подключения:", err)
		os.Exit(1)
	}
	defer conn.Close()

	bw := bufio.NewWriter(conn)
	_ = binary.Write(bw, binary.BigEndian, uint32(len(files)))

	for i, f := range files {
		fmt.Printf("(%d/%d) %s (%d байт)\n", i+1, len(files), f.RelPath, f.Size)
		if err := sendOne(bw, f); err != nil {
			fmt.Println("Ошибка отправки:", err)
			os.Exit(1)
		}
	}
	_ = bw.Flush()
	fmt.Println("Все файлы успешно отправлены.")
}

func sendOne(w *bufio.Writer, f fileEntry) error {
	nameBytes := []byte(f.RelPath)
	if len(nameBytes) == 0 || len(nameBytes) > maxNameLength {
		return fmt.Errorf("слишком длинное имя: %s", f.RelPath)
	}
	_ = binary.Write(w, binary.BigEndian, uint16(len(nameBytes)))
	_, _ = w.Write(nameBytes)
	_ = binary.Write(w, binary.BigEndian, uint64(f.Size))

	file, err := os.Open(f.AbsPath)
	if err != nil {
		return err
	}
	defer file.Close()

	bar := progressbar.NewOptions64(
		f.Size,
		progressbar.OptionSetDescription(filepath.Base(f.RelPath)),
		progressbar.OptionShowBytes(true),
		progressbar.OptionSetWidth(15),
		progressbar.OptionThrottle(100*time.Millisecond),
		progressbar.OptionShowCount(),
		progressbar.OptionOnCompletion(func() { fmt.Println() }),
	)

	_, err = io.Copy(io.MultiWriter(w, bar), file)
	return err
}

func runServerCmd(args []string) {
	fs := flag.NewFlagSet("server", flag.ExitOnError)
	addr := fs.String("addr", ":"+defaultPort, "адрес для прослушивания")
	outDir := fs.String("out", "./received", "куда сохранять")
	_ = fs.Parse(args)
	_ = os.MkdirAll(*outDir, 0o755)

	ln, _ := net.Listen("tcp", *addr)
	fmt.Println("Сервер слушает", *addr)
	for {
		c, _ := ln.Accept()
		go handleConn(c, *outDir)
	}
}

func handleConn(conn net.Conn, outDir string) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	var fileCount uint32
	_ = binary.Read(br, binary.BigEndian, &fileCount)
	for i := 0; i < int(fileCount); i++ {
		name, size, _ := readHeader(br)
		dstPath := filepath.Join(outDir, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(dstPath), 0o755)
		_ = receiveFile(br, dstPath, size)
		fmt.Println("Получен:", dstPath)
	}
}

func readHeader(r *bufio.Reader) (string, uint64, error) {
	var nameLen uint16
	_ = binary.Read(r, binary.BigEndian, &nameLen)
	nameBytes := make([]byte, nameLen)
	_, _ = io.ReadFull(r, nameBytes)
	var size uint64
	_ = binary.Read(r, binary.BigEndian, &size)
	return string(nameBytes), size, nil
}

func receiveFile(r *bufio.Reader, path string, size uint64) error {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f, _ := os.Create(path)
	defer f.Close()
	_, err := io.CopyN(f, r, int64(size))
	return err
}
func normalizeAddr(s string) string {
	if !strings.Contains(s, ":") {
		return s + ":" + defaultPort
	}
	return s
}
