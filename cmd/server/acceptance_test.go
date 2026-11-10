//go:build integration

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	v1 "github.com/apus-run/spine-layout/api/todo/v1"
	spinehttp "github.com/apus-run/spine/v3/transport/http"
	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

type runningService struct {
	httpURL     string
	grpcAddress string
	stop        func()
}

func startService(t *testing.T, binary, dsn string) runningService {
	t.Helper()
	listeners := make([]net.Listener, 2)
	for i := range listeners {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		listeners[i] = listener
	}
	httpAddress, grpcAddress := listeners[0].Addr().String(), listeners[1].Addr().String()
	for _, listener := range listeners {
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
	}
	directory := t.TempDir()
	configDirectory := filepath.Join(directory, "config")
	if err := os.Mkdir(configDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"server": map[string]any{
			"http": map[string]any{"addr": httpAddress, "timeout": "1s"},
			"grpc": map[string]any{"addr": grpcAddress, "timeout": "1s"},
		},
		"data": map[string]any{"database": map[string]any{"driver": "mysql", "source": "${DATABASE_SOURCE}", "auto_migrate": true, "debug": false}},
	}
	content, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(configDirectory, "config.json"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(directory, "server.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-conf", configDirectory)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "SPINE_") && !strings.HasPrefix(value, "KRATOS") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "SPINE_DATABASE_SOURCE="+dsn)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err = cmd.Start(); err != nil {
		logFile.Close()
		t.Fatal(err)
	}
	done := make(chan struct{})
	var processErr error
	go func() { processErr = cmd.Wait(); close(done) }()
	readLogs := func() string {
		data, _ := os.ReadFile(logPath)
		text := strings.ReplaceAll(string(data), dsn, "<database DSN>")
		if config, err := mysql.ParseDSN(dsn); err == nil && config.Passwd != "" {
			text = strings.ReplaceAll(text, config.Passwd, "<password>")
		}
		return text
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			select {
			case <-done:
			default:
				_ = cmd.Process.Signal(syscall.SIGTERM)
				select {
				case <-done:
				case <-time.After(10 * time.Second):
					_ = cmd.Process.Kill()
					<-done
					t.Error("server did not stop gracefully")
				}
			}
			if processErr != nil {
				t.Errorf("server exit: %v\n%s", processErr, readLogs())
			}
			if strings.Contains(readLogs(), "DATA RACE") {
				t.Errorf("server race detected\n%s", readLogs())
			}
			if t.Failed() {
				text := readLogs()
				if len(text) > 16000 {
					text = text[len(text)-16000:]
				}
				t.Logf("server log: %s", text)
			}
			_ = logFile.Close()
		})
	}
	t.Cleanup(stop)
	probe := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatalf("server exited during startup: %v\n%s", processErr, readLogs())
		default:
		}
		resp, err := probe.Get("http://" + httpAddress + "/v1/todos/list?page_size=1")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return runningService{httpURL: "http://" + httpAddress, grpcAddress: grpcAddress, stop: stop}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server readiness timed out\n%s", readLogs())
	return runningService{}
}

func httpClient(t *testing.T, ctx context.Context, address string) v1.TodoServiceHTTPClient {
	t.Helper()
	client, err := spinehttp.NewClient(ctx, spinehttp.WithEndpoint(address), spinehttp.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	return v1.NewTodoServiceHTTPClient(client)
}

func TestBusinessAcceptance(t *testing.T) {
	dsn := os.Getenv("SPINE_LAYOUT_TEST_DSN")
	if dsn == "" {
		t.Skip("set SPINE_LAYOUT_TEST_DSN to a dedicated MySQL database")
	}
	database, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err = database.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "server")
	build := exec.Command("go", "build", "-race", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build server: %v\n%s", err, output)
	}
	app := startService(t, binary, dsn)
	client := httpClient(t, ctx, app.httpURL)
	conn, err := grpc.NewClient(app.grpcAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	rpc := v1.NewTodoServiceClient(conn)
	prefix := "acceptance-" + uuid.NewString()
	t.Cleanup(func() { _, _ = database.Exec("DELETE FROM todos WHERE title LIKE ?", prefix+"%") })
	created, err := client.CreateTodo(ctx, &v1.CreateTodoRequest{Todo: &v1.Todo{Title: prefix + "-first", Content: "原始内容"}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := uuid.Parse(created.Id)
	if err != nil || parsed.Version() != 7 || created.CreatedAt == nil || created.UpdatedAt == nil || created.Status != v1.TodoStatus_TODO_STATUS_ACTIVE {
		t.Fatalf("create contract: %v", created)
	}
	got, err := rpc.GetTodo(ctx, &v1.GetTodoRequest{Id: created.Id})
	if err != nil || got.Content != "原始内容" {
		t.Fatalf("HTTP to gRPC persistence: %v %v", got, err)
	}
	updated, err := client.UpdateTodo(ctx, &v1.UpdateTodoRequest{Todo: &v1.Todo{Id: created.Id, Completed: true}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"completed"}}})
	if err != nil || !updated.GetCompleted() || updated.Title != created.Title || updated.Content != created.Content || !updated.CreatedAt.AsTime().Equal(created.CreatedAt.AsTime()) {
		t.Fatalf("partial update: %v %v", updated, err)
	}
	updated, err = client.UpdateTodo(ctx, &v1.UpdateTodoRequest{Todo: &v1.Todo{Id: created.Id}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"completed"}}})
	if err != nil || updated.GetCompleted() {
		t.Fatalf("clear boolean: %v %v", updated, err)
	}
	second, err := rpc.CreateTodo(ctx, &v1.CreateTodoRequest{Todo: &v1.Todo{Title: prefix + "-second", Completed: true}})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	token := ""
	for page := 0; page < 5; page++ {
		list, err := client.ListTodos(ctx, &v1.ListTodosRequest{PageSize: 1, PageToken: token, Filter: fmt.Sprintf("title:%q", prefix), OrderBy: "created_at"})
		if err != nil {
			t.Fatal(err)
		}
		for _, todo := range list.Todos {
			if seen[todo.Id] {
				t.Fatalf("duplicate page id %s", todo.Id)
			}
			seen[todo.Id] = true
		}
		token = list.NextPageToken
		if token == "" {
			break
		}
	}
	if len(seen) != 2 || !seen[created.Id] || !seen[second.Id] || token != "" {
		t.Fatalf("pagination: %v token=%q", seen, token)
	}
	list, err := client.ListTodos(ctx, &v1.ListTodosRequest{Filter: fmt.Sprintf("title:%q AND completed", prefix)})
	if err != nil || len(list.GetTodos()) != 1 || list.Todos[0].Id != second.Id {
		t.Fatalf("filter: %v %v", list, err)
	}
	for _, req := range []*v1.ListTodosRequest{{PageToken: "bad-token"}, {Filter: `unknown:"value"`}, {OrderBy: "content"}} {
		if _, err = client.ListTodos(ctx, req); !v1.IsTodoInvalidArgument(err) {
			t.Errorf("invalid query: %v", err)
		}
	}
	if _, err = client.GetTodo(ctx, &v1.GetTodoRequest{Id: "bad-id"}); !v1.IsTodoInvalidArgument(err) {
		t.Errorf("invalid id: %v", err)
	}
	if _, err = client.GetTodo(ctx, &v1.GetTodoRequest{Id: uuid.NewString()}); !v1.IsTodoNotFound(err) {
		t.Errorf("missing record: %v", err)
	}
	if _, err = client.UpdateTodo(ctx, &v1.UpdateTodoRequest{Todo: &v1.Todo{Id: created.Id, Title: prefix + "-should-not-change"}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"unknown"}}}); !v1.IsTodoInvalidArgument(err) {
		t.Errorf("invalid mask: %v", err)
	}
	watch, err := client.WatchTodos(ctx, &v1.WatchTodosRequest{Filter: fmt.Sprintf("title:%q", prefix), PageSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	snapshots := 0
	for {
		event, err := watch.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if event.Action != "snapshot" {
			t.Fatalf("snapshot event: %v", event)
		}
		snapshots++
	}
	if snapshots != 2 {
		t.Fatalf("snapshot count %d", snapshots)
	}
	syncStream, err := client.SyncTodos(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = syncStream.Send(&v1.SyncTodoRequest{Action: "create", Todo: &v1.Todo{Title: prefix + "-sync"}}); err != nil {
		t.Fatal(err)
	}
	event, err := syncStream.Recv()
	if err != nil || event.GetAction() != "created" {
		t.Fatalf("sync create: %v %v", event, err)
	}
	syncID := event.Todo.Id
	if err = syncStream.Send(&v1.SyncTodoRequest{Action: "update", Todo: &v1.Todo{Id: syncID, Completed: true}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"completed"}}}); err != nil {
		t.Fatal(err)
	}
	event, err = syncStream.Recv()
	if err != nil || event.GetAction() != "updated" || !event.GetTodo().GetCompleted() {
		t.Fatalf("sync update: %v %v", event, err)
	}
	if err = syncStream.Send(&v1.SyncTodoRequest{Action: "delete", Id: syncID}); err != nil {
		t.Fatal(err)
	}
	event, err = syncStream.Recv()
	if err != nil || event.GetAction() != "deleted" {
		t.Fatalf("sync delete: %v %v", event, err)
	}
	if err = syncStream.CloseSend(); err != nil {
		t.Fatal(err)
	}
	if _, err = syncStream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("sync EOF: %v", err)
	}
	if _, err = rpc.DeleteTodo(ctx, &v1.DeleteTodoRequest{Id: second.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.GetTodo(ctx, &v1.GetTodoRequest{Id: second.Id}); !v1.IsTodoNotFound(err) {
		t.Errorf("deleted record visible: %v", err)
	}
	var storedStatus int
	if err = database.QueryRowContext(ctx, "SELECT status FROM todos WHERE id = ?", second.Id).Scan(&storedStatus); err != nil || storedStatus != 2 {
		t.Fatalf("soft delete: status=%d err=%v", storedStatus, err)
	}
	runConcurrentReads(t, ctx, client, created.Id)
	app.stop()
	restarted := startService(t, binary, dsn)
	afterRestart := httpClient(t, ctx, restarted.httpURL)
	persisted, err := afterRestart.GetTodo(ctx, &v1.GetTodoRequest{Id: created.Id})
	if err != nil || persisted.Title != created.Title {
		t.Fatalf("restart persistence: %v %v", persisted, err)
	}
}

func runConcurrentReads(t *testing.T, ctx context.Context, client v1.TodoServiceHTTPClient, id string) {
	t.Helper()
	duration := 30 * time.Second
	if value := os.Getenv("SPINE_LAYOUT_LOAD_DURATION"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed <= 0 {
			t.Fatal("invalid SPINE_LAYOUT_LOAD_DURATION")
		}
		duration = parsed
	}
	deadline := time.Now().Add(duration)
	var wg sync.WaitGroup
	var mutex sync.Mutex
	var latencies []time.Duration
	failures := 0
	firstFailure := ""
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				start := time.Now()
				todo, err := client.GetTodo(ctx, &v1.GetTodoRequest{Id: id})
				elapsed := time.Since(start)
				mutex.Lock()
				latencies = append(latencies, elapsed)
				if err != nil || todo.GetId() != id {
					failures++
					if firstFailure == "" {
						firstFailure = fmt.Sprintf("response=%v error=%v", todo, err)
					}
				}
				mutex.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(latencies) == 0 {
		t.Fatal("no load requests completed")
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	percentile := func(percent int) time.Duration { return latencies[(len(latencies)-1)*percent/100] }
	t.Logf("load duration=%s concurrency=8 requests=%d failures=%d p50=%s p95=%s p99=%s", duration, len(latencies), failures, percentile(50), percentile(95), percentile(99))
	if failures != 0 {
		t.Fatalf("load failures: %d, first: %s", failures, firstFailure)
	}
}
