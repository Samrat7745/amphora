package testkit

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Plain-docker containers for the persistence tests (Testcontainers does not work with Docker 29).
// Containers are named ampB-<kind>-<pid>-<n> and removed by Lazy.Close (call it from TestMain).

// Images used by the Java integration tests (or the closest pinned equivalents).
const (
	PostgresImage = "postgres:11.1" // ReusablePostgreSQLContainer
	RedisImage    = "redis:7.0"
	MinioImage    = "quay.io/minio/minio:RELEASE.2022-04-16T04-26-02Z"

	MinioAccessKey = "dummy-key"
	MinioSecretKey = "dummy-secret"
)

var seq struct {
	sync.Mutex
	n int
}

// DockerAvailable reports whether the docker CLI can talk to a daemon.
func DockerAvailable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	return exec.Command("docker", "info", "--format", "{{.ServerVersion}}").Run() == nil
}

func docker(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// run starts a detached container publishing containerPort on a random loopback port; it returns
// the host:port and a stop function.
func run(kind string, containerPort int, image string, env []string, cmd ...string) (string, func(), error) {
	if !DockerAvailable() {
		return "", nil, fmt.Errorf("docker is not available")
	}
	seq.Lock()
	seq.n++
	name := fmt.Sprintf("ampB-%s-%d-%d", kind, os.Getpid(), seq.n)
	seq.Unlock()
	args := []string{"run", "-d", "--name", name, "-p", fmt.Sprintf("127.0.0.1::%d", containerPort)}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, image)
	args = append(args, cmd...)
	if out, err := docker(args...); err != nil {
		_, _ = docker("rm", "-f", name)
		return "", nil, fmt.Errorf("docker run %s: %v: %s", image, err, out)
	}
	stop := func() { _, _ = docker("rm", "-f", "-v", name) }
	out, err := docker("port", name, fmt.Sprintf("%d/tcp", containerPort))
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("docker port: %v: %s", err, out)
	}
	addr := strings.Split(out, "\n")[0]
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		addr = "127.0.0.1" + addr[i:]
	}
	return addr, stop, nil
}

func waitTCP(addr string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = c.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("%s not reachable after %s", addr, d)
}

// StartPostgres starts postgres and returns a DSN once it accepts queries.
func StartPostgres() (string, func(), error) {
	addr, stop, err := run("pg", 5432, PostgresImage, []string{"POSTGRES_PASSWORD=pw", "POSTGRES_USER=amphora", "POSTGRES_DB=amphora"})
	if err != nil {
		return "", nil, err
	}
	dsn := "postgres://amphora:pw@" + addr + "/amphora?sslmode=disable"
	// the image restarts the server once after initialisation: require two successful queries in a row.
	deadline := time.Now().Add(90 * time.Second)
	ok := 0
	for time.Now().Before(deadline) {
		if pgReady(dsn) {
			if ok++; ok >= 2 {
				return dsn, stop, nil
			}
		} else {
			ok = 0
		}
		time.Sleep(500 * time.Millisecond)
	}
	stop()
	return "", nil, fmt.Errorf("postgres did not become ready")
}

func pgReady(dsn string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return false
	}
	defer c.Close(context.Background())
	var one int
	return c.QueryRow(ctx, "SELECT 1").Scan(&one) == nil
}

func minioReady(addr string) bool {
	c, err := minio.New(addr, &minio.Options{Creds: credentials.NewStaticV4(MinioAccessKey, MinioSecretKey, "")})
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = c.ListBuckets(ctx)
	return err == nil
}

// StartRedis starts redis and returns host:port.
func StartRedis() (string, func(), error) {
	addr, stop, err := run("redis", 6379, RedisImage, nil)
	if err != nil {
		return "", nil, err
	}
	if err := waitTCP(addr, 30*time.Second); err != nil {
		stop()
		return "", nil, err
	}
	return addr, stop, nil
}

// StartMinio starts minio (credentials MinioAccessKey/MinioSecretKey) and returns host:port.
func StartMinio() (string, func(), error) {
	addr, stop, err := run("minio", 9000, MinioImage, []string{"MINIO_ROOT_USER=" + MinioAccessKey, "MINIO_ROOT_PASSWORD=" + MinioSecretKey}, "server", "/data")
	if err != nil {
		return "", nil, err
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if minioReady(addr) {
			return addr, stop, nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	stop()
	return "", nil, fmt.Errorf("minio did not become ready")
}

// Lazy starts one container on first use and removes it on Close.
type Lazy struct {
	start func() (string, func(), error)
	once  sync.Once
	addr  string
	stop  func()
	err   error
}

// NewLazy wraps one of the Start* functions.
func NewLazy(start func() (string, func(), error)) *Lazy { return &Lazy{start: start} }

// Get returns the address (DSN for postgres) or skips the test when docker/the image is unavailable.
func (l *Lazy) Get(t interface {
	Helper()
	Skipf(string, ...any)
}) string {
	t.Helper()
	l.once.Do(func() { l.addr, l.stop, l.err = l.start() })
	if l.err != nil {
		t.Skipf("container unavailable: %v", l.err)
	}
	return l.addr
}

// Close removes the container if it was started.
func (l *Lazy) Close() {
	if l.stop != nil {
		l.stop()
	}
}
