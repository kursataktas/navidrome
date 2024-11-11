package metrics

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sync"
	"time"

	"github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/core/metrics/insights"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"golang.org/x/time/rate"
)

type Insights interface {
	Collect(ctx context.Context) string
}

var (
	insightsID    string
	libraryUpdate = rate.Sometimes{Interval: 10 * time.Minute}
)

type insightsCollector struct {
	ds model.DataStore
}

func NewInsights(ds model.DataStore) Insights {
	id, err := ds.Property(context.TODO()).Get(consts.InsightsID)
	if err != nil {
		log.Trace("Could not get Insights ID from DB", err)
		id = uuid.NewString()
		err = ds.Property(context.TODO()).Put(consts.InsightsID, id)
		if err != nil {
			log.Trace("Could not save Insights ID to DB", err)
		}
	}
	insightsID = id
	return &insightsCollector{ds: ds}
}

func buildInfo() (map[string]string, string) {
	bInfo := map[string]string{}
	var version string
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Value == "" {
				continue
			}
			bInfo[setting.Key] = setting.Value
		}
		version = info.GoVersion
	}
	return bInfo, version
}

func getFSInfo(path string) *insights.FSInfo {
	var info insights.FSInfo

	// Normalize the path
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	absPath = filepath.Clean(absPath)

	fsType, err := getFilesystemType(absPath)
	if err != nil {
		return nil
	}
	info.Type = fsType
	return &info
}

var staticData = sync.OnceValue(func() insights.Data {
	// Basic info
	data := insights.Data{
		InsightsID: insightsID,
		Version:    consts.Version,
	}

	// Build info
	data.Build.Settings, data.Build.GoVersion = buildInfo()

	// OS info
	data.OS.Type = runtime.GOOS
	data.OS.Arch = runtime.GOARCH
	data.OS.NumCPU = runtime.NumCPU()
	data.OS.Version, data.OS.Distro = getOSVersion()

	// FS info
	data.FS.Music = getFSInfo(conf.Server.MusicFolder)
	data.FS.Data = getFSInfo(conf.Server.DataFolder)
	if conf.Server.CacheFolder != "" {
		data.FS.Cache = getFSInfo(conf.Server.CacheFolder)
	}
	if conf.Server.Backup.Path != "" {
		data.FS.Backup = getFSInfo(conf.Server.Backup.Path)
	}

	// Config info
	data.Config.LogLevel = conf.Server.LogLevel
	data.Config.LogFileConfigured = conf.Server.LogFile != ""
	data.Config.TLSConfigured = conf.Server.TLSCert != "" && conf.Server.TLSKey != ""
	data.Config.DefaultBackgroundURL = conf.Server.UILoginBackgroundURL == consts.DefaultUILoginBackgroundURL
	data.Config.EnableArtworkPrecache = conf.Server.EnableArtworkPrecache
	data.Config.EnableCoverAnimation = conf.Server.EnableCoverAnimation
	data.Config.EnableDownloads = conf.Server.EnableDownloads
	data.Config.EnableExternalServices = conf.Server.EnableExternalServices
	data.Config.EnableSharing = conf.Server.EnableSharing
	data.Config.EnableStarRating = conf.Server.EnableStarRating
	data.Config.EnableLastFM = conf.Server.LastFM.Enabled
	data.Config.EnableListenBrainz = conf.Server.ListenBrainz.Enabled
	data.Config.EnableMediaFileCoverArt = conf.Server.EnableMediaFileCoverArt
	data.Config.EnableSpotify = conf.Server.Spotify.ID != ""
	data.Config.EnableJukebox = conf.Server.Jukebox.Enabled
	data.Config.EnablePrometheus = conf.Server.Prometheus.Enabled
	data.Config.TranscodingCacheSize = conf.Server.TranscodingCacheSize
	data.Config.ImageCacheSize = conf.Server.ImageCacheSize
	data.Config.ScanSchedule = conf.Server.ScanSchedule
	data.Config.SessionTimeout = conf.Server.SessionTimeout
	data.Config.SearchFullString = conf.Server.SearchFullString
	data.Config.RecentlyAddedByModTime = conf.Server.RecentlyAddedByModTime
	data.Config.PreferSortTags = conf.Server.PreferSortTags
	data.Config.BackupSchedule = conf.Server.Backup.Schedule
	data.Config.BackupCount = conf.Server.Backup.Count
	data.Config.DevActivityPanel = conf.Server.DevActivityPanel
	data.Config.DevAutoLoginUsername = conf.Server.DevAutoLoginUsername != ""
	data.Config.DevAutoCreateAdminPassword = conf.Server.DevAutoCreateAdminPassword != ""

	return data
})

func (s insightsCollector) Collect(ctx context.Context) string {
	data := staticData()
	data.Uptime = time.Since(consts.ServerStart).Milliseconds() / 1000
	libraryUpdate.Do(func() {
		data.Library.Tracks, _ = s.ds.MediaFile(ctx).CountAll()
		data.Library.Albums, _ = s.ds.Album(ctx).CountAll()
		data.Library.Artists, _ = s.ds.Artist(ctx).CountAll()
		data.Library.Playlists, _ = s.ds.Playlist(ctx).Count()
		data.Library.Shares, _ = s.ds.Share(ctx).CountAll()
		data.Library.Radios, _ = s.ds.Radio(ctx).Count()
		data.Library.ActiveUsers, _ = s.ds.User(ctx).CountAll(model.QueryOptions{
			Filters: squirrel.Gt{"last_access_at": time.Now().Add(-7 * 24 * time.Hour)},
		})
	})

	// Marshal to JSON
	resp, err := json.Marshal(data)
	if err != nil {
		log.Trace(ctx, "Could not marshal Insights data", err)
		return ""
	}
	return string(resp)
}
