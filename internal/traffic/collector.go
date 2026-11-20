package traffic

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type collector struct {
	routes Targets
	file   os.FileInfo
	state  snapshot
	dirty  bool
}

// reload 比较原子替换后的文件身份，只在项目集合发生变化时重新解码。
func (c *collector) reload(path string) {
	info, err := os.Stat(path)
	if err != nil || c.file != nil && os.SameFile(c.file, info) {
		return
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var routes Targets
	if json.Unmarshal(body, &routes) == nil {
		c.routes, c.file = routes, info
	}
}

// record 只统计已登记的正式域名，忽略预览、未知主机和创建时间以前的延迟消息。
func (c *collector) record(line string, now time.Time) {
	_, fields, ok := strings.Cut(line, "stacknest: sn1 ")
	if !ok {
		return
	}
	values := strings.Fields(fields)
	if len(values) != 4 {
		return
	}
	target, ok := c.routes[values[1]]
	if !ok {
		return
	}
	seconds, fraction, ok := strings.Cut(values[0], ".")
	if !ok || len(fraction) != 3 {
		return
	}
	t, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil {
		return
	}
	ms, err := strconv.Atoi(fraction)
	if err != nil {
		return
	}
	at := time.Unix(t, int64(ms)*1e6).UTC()
	if at.Before(target.CreatedAt.Truncate(time.Millisecond)) || at.Before(now.Add(-time.Minute)) || at.After(now.Add(time.Minute)) {
		return
	}
	code, err := strconv.Atoi(values[2])
	if err != nil || code < 100 || code > 599 || values[3] != "0" && values[3] != "1" {
		return
	}
	date := at.Format(time.DateOnly)
	rows := c.state.Days[date]
	if rows == nil {
		rows = map[string]Counts{}
		c.state.Days[date] = rows
	}
	value := rows[target.ID]
	value.Requests++
	if values[3] == "1" {
		value.Pages++
	}
	if code >= 500 {
		value.ServerErrors++
	} else if code >= 400 {
		value.ClientErrors++
	}
	rows[target.ID] = value
	c.dirty = true
}

// flush 淘汰第八天起的数据，并把时间和计数原子保存到实例数据卷。
func (c *collector) flush(path string, now time.Time) error {
	oldest := now.AddDate(0, 0, -6).Format(time.DateOnly)
	for date := range c.state.Days {
		if date < oldest {
			delete(c.state.Days, date)
		}
	}
	c.state.UpdatedAt = now
	body, err := json.Marshal(c.state)
	if err != nil {
		return err
	}
	if err = atomicFile(path, body); err != nil {
		return err
	}
	c.dirty = false
	return nil
}

// Collect 在独立 Runner 中接收只含计数信息的 Unix 数据报，备份暂停 API 时继续运行。
func Collect(ctx context.Context, dir string) error {
	socket := filepath.Join(dir, "traffic.sock")
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("访问统计 socket 路径被占用")
		}
		if err = os.Remove(socket); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer os.Remove(socket)
	defer conn.Close()
	if err = os.Chmod(socket, 0666); err != nil {
		return err
	}
	statePath := filepath.Join(dir, "traffic-counts.json")
	routesPath := filepath.Join(dir, "traffic-routes.json")
	c := collector{state: snapshot{Since: time.Now().UTC(), Days: map[string]map[string]Counts{}}}
	body, err := os.ReadFile(statePath)
	if errors.Is(err, os.ErrNotExist) {
		body, err = os.ReadFile(filepath.Join(dir, "logs", "traffic-at-backup.json"))
	}
	if err == nil {
		if err = json.Unmarshal(body, &c.state); err != nil || c.state.Days == nil {
			return errors.New("访问统计快照无效")
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var buffer [512]byte
	var nextCheck, lastSave time.Time
	for ctx.Err() == nil {
		now := time.Now().UTC()
		if !now.Before(nextCheck) {
			c.reload(routesPath)
			if now.Sub(lastSave) >= 30*time.Second || c.dirty && now.Sub(lastSave) >= 3*time.Second {
				if err = c.flush(statePath, now); err != nil && !errors.Is(err, os.ErrNotExist) {
					slog.Warn("保存访问统计失败", "error", err)
				}
				lastSave = now
			}
			nextCheck = now.Add(time.Second)
			if err = conn.SetReadDeadline(nextCheck); err != nil {
				return err
			}
		}
		n, _, err := conn.ReadFromUnix(buffer[:])
		if errors.Is(err, os.ErrDeadlineExceeded) {
			continue
		}
		if err != nil {
			return err
		}
		if n < len(buffer) {
			c.reload(routesPath)
			c.record(string(buffer[:n]), time.Now().UTC())
		}
	}
	return c.flush(statePath, time.Now().UTC())
}
