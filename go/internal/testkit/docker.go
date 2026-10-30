package testkit

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Containers for the persistence tests, started with testcontainers-go (the Go port of the Testcontainers library the
// Java tests used; it negotiates the Docker API version, so it works with current Docker engines). Each Start* returns
// the address and a stop function; Lazy starts one on first use and terminates it on Close (call it from TestMain).

// Images used by the Java integration tests (or the closest pinned equivalents).
const (
	PostgresImage = "postgres:11.1" // ReusablePostgreSQLContainer
	RedisImage    = "redis:7.0"
	MinioImage    = "quay.io/minio/minio:RELEASE.2022-04-16T04-26-02Z"

	MinioAccessKey = "dummy-key"
	MinioSecretKey = "dummy-secret"
)

// DockerAvailable reports whether a Docker daemon can be reached.
func DockerAvailable() bool {
	p, err := testcontainers.NewDockerProvider()
	if err != nil {
		return false
	}
	defer p.Close()
	return p.Health(context.Background()) == nil
}

// run starts a container exposing port on a random host port; it returns host:port and a stop function.
func run(req testcontainers.ContainerRequest, port string) (string, func(), error) {
	if !DockerAvailable() {
		return "", nil, fmt.Errorf("docker is not available")
	}
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	stop := func() {
		if c != nil {
			_ = c.Terminate(context.Background())
		}
	}
	if err != nil {
		stop()
		return "", nil, fmt.Errorf("starting %s: %w", req.Image, err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		stop()
		return "", nil, err
	}
	mapped, err := c.MappedPort(ctx, port)
	if err != nil {
		stop()
		return "", nil, err
	}
	return net.JoinHostPort(host, mapped.Port()), stop, nil
}

// StartPostgres starts postgres and returns a DSN once it accepts queries.
func StartPostgres() (string, func(), error) {
	addr, stop, err := run(testcontainers.ContainerRequest{
		Image:        PostgresImage,
		ExposedPorts: []string{"5432/tcp"},
		Env:          map[string]string{"POSTGRES_PASSWORD": "pw", "POSTGRES_USER": "amphora", "POSTGRES_DB": "amphora"},
		WaitingFor:   wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90 * time.Second),
	}, "5432/tcp")
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
	return run(testcontainers.ContainerRequest{
		Image:        RedisImage,
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForLog("Ready to accept connections").WithStartupTimeout(60 * time.Second),
	}, "6379/tcp")
}

// StartMinio starts minio (credentials MinioAccessKey/MinioSecretKey) and returns host:port.
func StartMinio() (string, func(), error) {
	addr, stop, err := run(testcontainers.ContainerRequest{
		Image:        MinioImage,
		ExposedPorts: []string{"9000/tcp"},
		Env:          map[string]string{"MINIO_ROOT_USER": MinioAccessKey, "MINIO_ROOT_PASSWORD": MinioSecretKey},
		Cmd:          []string{"server", "/data"},
		WaitingFor:   wait.ForListeningPort("9000/tcp").WithStartupTimeout(60 * time.Second),
	}, "9000/tcp")
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
