package files

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"chattp/internal/auth"
	"chattp/internal/chat"
	"chattp/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const maxSize = 50 << 20

var mimeAllow = map[string]bool{"application/pdf": true, "image/png": true, "image/jpeg": true, "image/gif": true, "text/plain": true, "application/zip": true}

type Store struct {
	*platform.Services
	client          *minio.Client
	bucket, scanner string
}

func New(s *platform.Services) (*Store, error) {
	key, secret := os.Getenv("MINIO_ACCESS_KEY"), os.Getenv("MINIO_SECRET_KEY")
	if key == "" || secret == "" {
		return nil, errors.New("MinIO credentials required")
	}
	client, err := minio.New(platform.Env("MINIO_ENDPOINT", "minio:9000"), &minio.Options{Creds: credentials.NewStaticV4(key, secret, ""), Secure: os.Getenv("MINIO_TLS") == "true"})
	if err != nil {
		return nil, err
	}
	return &Store{s, client, platform.Env("MINIO_BUCKET", "chattp"), platform.Env("CLAMAV_ADDR", "clamav:3310")}, nil
}

func (s *Store) EnsureBucket(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		exists, err := s.client.BucketExists(ctx, s.bucket)
		if err == nil && exists {
			return nil
		}
		if err == nil {
			if err = s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err == nil {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return errors.Join(ctx.Err(), err)
		case <-ticker.C:
		}
	}
}

func (s *Store) Upload(w http.ResponseWriter, r *http.Request, u auth.User) {
	ctx, channel := r.Context(), chat.Param(r, "channel")
	if !chat.CanRead(ctx, s.DB, channel, u.ID) {
		platform.Error(w, 403, "upload_denied")
		return
	}
	var allowed bool
	err := s.DB.QueryRow(ctx, "SELECT kind<>'announcement' AND archived_at IS NULL FROM channels WHERE id=$1", channel).Scan(&allowed)
	if err != nil || !allowed {
		platform.Error(w, 403, "upload_denied")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSize+(1<<20))
	if err = r.ParseMultipartForm(1 << 20); err != nil {
		platform.Error(w, 400, "invalid_upload")
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, info, err := r.FormFile("file")
	if err != nil {
		platform.Error(w, 400, "file_required")
		return
	}
	defer file.Close()
	if info.Size < 1 || info.Size > maxSize {
		platform.Error(w, 413, "invalid_file_size")
		return
	}
	name := path.Base(strings.ReplaceAll(info.Filename, "\\", "/"))
	if name == "." || name == "/" || len(name) > 240 {
		platform.Error(w, 400, "invalid_file_name")
		return
	}
	prefix := make([]byte, 512)
	n, err := file.Read(prefix)
	if err != nil && !errors.Is(err, io.EOF) {
		platform.Error(w, 400, "invalid_file")
		return
	}
	detected, _, _ := mime.ParseMediaType(http.DetectContentType(prefix[:n]))
	if !mimeAllow[detected] {
		platform.Error(w, 415, "file_type_denied")
		return
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		platform.Error(w, 400, "invalid_file")
		return
	}
	id := chat.ID()
	if _, err = s.client.PutObject(ctx, s.bucket, id, file, info.Size, minio.PutObjectOptions{ContentType: detected}); err != nil {
		chat.Failure(w, err)
		return
	}
	if _, err = s.DB.Exec(ctx, "INSERT INTO files(id,channel_id,uploader_id,name,mime,size) VALUES($1,$2,$3,$4,$5,$6)", id, channel, u.ID, name, detected, info.Size); err != nil {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.client.RemoveObject(cleanup, s.bucket, id, minio.RemoveObjectOptions{})
		chat.Failure(w, err)
		return
	}
	platform.JSON(w, 201, map[string]string{"id": id, "name": name, "status": "pending"})
}

func (s *Store) Download(w http.ResponseWriter, r *http.Request, u auth.User) {
	s.serveFile(w, r, u, false)
}

func (s *Store) Thumbnail(w http.ResponseWriter, r *http.Request, u auth.User) {
	s.serveFile(w, r, u, true)
}

var thumbnailSlots = make(chan struct{}, 2)

func (s *Store) serveFile(w http.ResponseWriter, r *http.Request, u auth.User, thumbnail bool) {
	ctx := r.Context()
	id := chat.Param(r, "file")
	var name, status, mediaType string
	var size int64
	err := s.DB.QueryRow(ctx, `SELECT f.name,f.status,f.size,f.mime FROM files f
 WHERE f.id=$1 AND EXISTS(SELECT 1 FROM channel_members cm WHERE cm.channel_id=f.channel_id AND cm.user_id=$2)
 AND (f.uploader_id=$2 OR EXISTS(SELECT 1 FROM message_files mf JOIN messages m ON m.id=mf.message_id WHERE mf.file_id=f.id AND m.deleted_at IS NULL))`, id, u.ID).Scan(&name, &status, &size, &mediaType)
	if err != nil {
		chat.Failure(w, err)
		return
	}
	if status != "clean" {
		platform.Error(w, 409, "file_unavailable")
		return
	}
	if thumbnail {
		if mediaType != "image/jpeg" && mediaType != "image/png" && mediaType != "image/gif" {
			platform.Error(w, 415, "thumbnail_unavailable")
			return
		}
		select {
		case thumbnailSlots <- struct{}{}:
			defer func() { <-thumbnailSlots }()
		case <-ctx.Done():
			return
		}
	}
	object, err := s.client.GetObject(ctx, s.bucket, id, minio.GetObjectOptions{})
	if err != nil {
		chat.Failure(w, err)
		return
	}
	defer object.Close()
	if _, err = object.Stat(); err != nil {
		chat.Failure(w, err)
		return
	}
	if thumbnail {
		writeThumbnail(w, object)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	_, _ = io.Copy(w, object)
}

func writeThumbnail(w http.ResponseWriter, object io.ReadSeeker) {
	config, _, err := image.DecodeConfig(object)
	const maxPixels = 24_000_000
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > maxPixels/config.Height {
		platform.Error(w, 422, "thumbnail_unavailable")
		return
	}
	if _, err = object.Seek(0, io.SeekStart); err != nil {
		chat.Failure(w, err)
		return
	}
	source, _, err := image.Decode(object)
	if err != nil {
		platform.Error(w, 422, "thumbnail_unavailable")
		return
	}
	bounds := source.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	const maxEdge = 384
	if longest := max(width, height); longest > maxEdge {
		width = max(1, width*maxEdge/longest)
		height = max(1, height*maxEdge/longest)
	}
	resized := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			resized.Set(x, y, source.At(bounds.Min.X+x*bounds.Dx()/width, bounds.Min.Y+y*bounds.Dy()/height))
		}
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_ = jpeg.Encode(w, resized, &jpeg.Options{Quality: 82})
}

func (s *Store) Scan(ctx context.Context) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id, channel string
	err = tx.QueryRow(ctx, "SELECT id,channel_id FROM files WHERE status='pending' ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED").Scan(&id, &channel)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	object, err := s.client.GetObject(ctx, s.bucket, id, minio.GetObjectOptions{})
	if err != nil {
		return err
	}
	defer object.Close()
	status, err := s.scanObject(ctx, object)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE files SET status=$2,scanned_at=now() WHERE id=$1", id, status); err != nil {
		return err
	}
	if status == "infected" {
		if _, err = tx.Exec(ctx, "INSERT INTO audit_logs(action,target) VALUES('file.infected',$1)", id); err != nil {
			return err
		}
	}
	if err = chat.AddEvent(ctx, tx, channel, "file.updated", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) PurgeInfected(ctx context.Context) error {
	var id string
	err := s.DB.QueryRow(ctx, "SELECT id FROM files WHERE status='infected' AND scanned_at IS NOT NULL ORDER BY scanned_at LIMIT 1").Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = s.client.RemoveObject(ctx, s.bucket, id, minio.RemoveObjectOptions{}); err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, "UPDATE files SET scanned_at=NULL WHERE id=$1 AND status='infected'", id)
	return err
}

func (s *Store) PurgeExpired(ctx context.Context) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx, "SELECT file_id FROM file_deletions ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED").Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = s.client.RemoveObject(ctx, s.bucket, id, minio.RemoveObjectOptions{}); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM file_deletions WHERE file_id=$1", id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "DELETE FROM files WHERE id=$1 AND status='expired'", id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) scanObject(ctx context.Context, object io.Reader) (string, error) {
	conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", s.scanner)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	deadline, _ := ctx.Deadline()
	if !deadline.IsZero() {
		_ = conn.SetDeadline(deadline)
	}
	writer := bufio.NewWriter(conn)
	if _, err = writer.WriteString("zINSTREAM\x00"); err != nil {
		return "", err
	}
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := object.Read(buffer)
		if n > 0 {
			if err = binary.Write(writer, binary.BigEndian, uint32(n)); err != nil {
				return "", err
			}
			if _, err = writer.Write(buffer[:n]); err != nil {
				return "", err
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if err = binary.Write(writer, binary.BigEndian, uint32(0)); err != nil {
		return "", err
	}
	if err = writer.Flush(); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(io.LimitReader(conn, 2048)).ReadString(0)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(line, " FOUND\x00") {
		return "infected", nil
	}
	if strings.HasSuffix(line, " OK\x00") {
		return "clean", nil
	}
	return "", errors.New("ClamAV returned scan error")
}
