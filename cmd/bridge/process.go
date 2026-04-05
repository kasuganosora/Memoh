package main

import (
	"bytes"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	pb "github.com/memohai/memoh/internal/workspace/bridgepb"
)

const (
	maxOutputBytes  = 1 * 1024 * 1024 // 1 MB cap per stream
	processTTL      = 1 * time.Hour
	cleanupInterval = 5 * time.Minute
)

// cappedBuffer is a bytes.Buffer that discards writes when it exceeds maxSize.
// When full, it keeps the tail of the data.
type cappedBuffer struct {
	buf     bytes.Buffer
	maxSize int
	mu      sync.Mutex
}

func newCappedBuffer(maxSize int) *cappedBuffer {
	return &cappedBuffer{maxSize: maxSize}
}

func (cb *cappedBuffer) Write(p []byte) (n int, err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	// If a single write exceeds maxSize, keep only the tail of p.
	if len(p) > cb.maxSize {
		cb.buf.Reset()
		cb.buf.Write(p[len(p)-cb.maxSize:])
		return len(p), nil
	}
	if cb.buf.Len()+len(p) > cb.maxSize {
		excess := cb.buf.Len() + len(p) - cb.maxSize
		if excess > 0 && excess <= cb.buf.Len() {
			cb.buf.Next(excess)
		} else {
			cb.buf.Reset()
		}
	}
	return cb.buf.Write(p)
}

func (cb *cappedBuffer) String() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.buf.String()
}

// backgroundProcess tracks a detached process.
type backgroundProcess struct {
	id         string
	command    string
	workDir    string
	stdout     *cappedBuffer
	stderr     *cappedBuffer
	exitCode   int32
	exited     bool
	startedAt  time.Time
	finishedAt time.Time
	done       chan struct{}
	mu         sync.Mutex
}

func (p *backgroundProcess) toResponse() *pb.ExecStatusResponse {
	p.mu.Lock()
	defer p.mu.Unlock()

	resp := &pb.ExecStatusResponse{
		Command:   p.command,
		Stdout:    p.stdout.String(),
		Stderr:    p.stderr.String(),
		StartedAt: p.startedAt.Unix(),
	}

	if p.exited {
		resp.Status = pb.ExecStatusResponse_EXITED
		resp.ExitCode = p.exitCode
		resp.FinishedAt = p.finishedAt.Unix()
	} else {
		resp.Status = pb.ExecStatusResponse_RUNNING
	}

	return resp
}

// processManager manages background processes.
type processManager struct {
	mu    sync.RWMutex
	procs map[string]*backgroundProcess
}

func newProcessManager() *processManager {
	pm := &processManager{
		procs: make(map[string]*backgroundProcess),
	}
	go pm.cleanupLoop()
	return pm
}

// adopt registers a running process for background monitoring.
// The tee goroutines in execPipe continue reading from the pipes into the
// shared cappedBuffers, so adopt does NOT receive pipe readers.
//
// waitCh carries the exit code from the single cmd.Wait() call in execPipe.
// adopt reads from it instead of calling Wait itself, avoiding a double-Wait race.
func (pm *processManager) adopt(
	command, workDir string,
	startedAt time.Time,
	stdoutBuf, stderrBuf *cappedBuffer,
	waitCh <-chan waitResult,
) string {
	id := uuid.New().String()[:12]

	bp := &backgroundProcess{
		id:        id,
		command:   command,
		workDir:   workDir,
		stdout:    stdoutBuf,
		stderr:    stderrBuf,
		startedAt: startedAt,
		done:      make(chan struct{}),
	}

	pm.mu.Lock()
	pm.procs[id] = bp
	pm.mu.Unlock()

	// Read the exit code from the single cmd.Wait() goroutine in execPipe.
	go func() {
		defer close(bp.done)
		wr := <-waitCh

		bp.mu.Lock()
		bp.exited = true
		bp.finishedAt = time.Now()
		bp.exitCode = wr.exitCode
		bp.mu.Unlock()
	}()

	return id
}

func (pm *processManager) lookup(id string) (*backgroundProcess, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	p, ok := pm.procs[id]
	return p, ok
}

func (pm *processManager) remove(id string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	delete(pm.procs, id)
}

func (pm *processManager) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		pm.mu.Lock()
		for id, p := range pm.procs {
			p.mu.Lock()
			if p.exited && time.Since(p.finishedAt) > processTTL {
				delete(pm.procs, id)
				slog.Debug("cleaned up background process", "exec_id", id)
			}
			p.mu.Unlock()
		}
		pm.mu.Unlock()
	}
}
