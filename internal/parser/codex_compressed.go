package parser

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"
)

func codexCompressed(path string) bool { return strings.HasSuffix(path, ".jsonl.zst") }
func codexPlainName(path string) string {
	if codexCompressed(path) {
		return strings.TrimSuffix(path, ".zst")
	}
	return path
}

// Decode into a private, disposable snapshot so the existing bounded JSONL
// parser retains ReaderAt semantics. The source is never expanded in place.
// Limits bound decoder memory and scratch disk; corrupt/truncated streams fail
// before any parsed result is published.
func decodeCodexSnapshot(ctx context.Context, f *os.File, info os.FileInfo) (*os.File, os.FileInfo, string, func(), error) {
	empty := func() {}
	hash := sha256.New()
	reader := io.TeeReader(io.NewSectionReader(f, 0, info.Size()), hash)
	decoder, err := zstd.NewReader(checkedContextReader{ctx: ctx, reader: reader}, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true), zstd.WithDecoderMaxMemory(512<<20), zstd.WithDecoderMaxWindow(256<<20))
	if err != nil {
		return nil, nil, "", empty, err
	}
	defer decoder.Close()
	tmp, err := os.CreateTemp("", "agentsview-codex-*.jsonl")
	if err != nil {
		return nil, nil, "", empty, err
	}
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }
	const limit int64 = 16 << 30
	n, err := io.Copy(tmp, io.LimitReader(checkedContextReader{ctx: ctx, reader: decoder}, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("Codex decoded snapshot exceeds size limit")
	}
	if err != nil {
		cleanup()
		return nil, nil, "", empty, err
	}
	if _, err = tmp.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, nil, "", empty, err
	}
	decoded, err := tmp.Stat()
	if err != nil {
		cleanup()
		return nil, nil, "", empty, err
	}
	after, err := f.Stat()
	if err != nil || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		cleanup()
		return nil, nil, "", empty, fmt.Errorf("Codex compressed source changed during read")
	}
	return tmp, decoded, hex.EncodeToString(hash.Sum(nil)), cleanup, nil
}

func (p *codexProvider) parseCodexSessionSnapshotStreaming(ctx context.Context, path, machine string, includeExec bool, f *os.File, info os.FileInfo, sink CodexSessionSink) (*ParsedSession, []ParsedMessage, codexCursorState, bool, []byte, string, string, error) {
	if !codexCompressed(path) {
		return p.parseCodexPlainSnapshotStreaming(ctx, path, machine, includeExec, f, info, sink)
	}
	decoded, decodedInfo, hash, cleanup, err := decodeCodexSnapshot(ctx, f, info)
	if err != nil {
		return nil, nil, codexCursorState{}, false, nil, "", "", err
	}
	defer cleanup()
	sess, msgs, _, _, _, _, retry, err := p.parseCodexPlainSnapshotStreaming(ctx, path, machine, includeExec, decoded, decodedInfo, sink)
	if sess != nil {
		inode, device := sourceFileIdentityForFile(f, info)
		changeTime, _ := codexIndexChangeTimeForFile(f, info)
		sess.File = FileInfo{Path: path, Size: info.Size(), Mtime: p.sources.metadata.EffectiveMtime(path, info.ModTime().UnixNano()), Hash: hash, Inode: int64(inode), Device: int64(device), ChangeTime: changeTime}
	}
	// Compressed byte offsets cannot be used as JSONL append checkpoints.
	return sess, msgs, codexCursorState{}, false, nil, "", retry, err
}
