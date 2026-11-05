package model

// StreamInfo 是播放模型里一条可选的清晰度（方案 §5.1 的「清晰度标签、codec」）。
//
// 刻意**不含 URL**：CDN 地址与密钥材料都不进 DTO。真正下发地址的只有
// SourceDTO（direct 直链或本地 127.0.0.1 源），由 internal/player 与
// internal/localsrv 负责生成。
type StreamInfo struct {
	Definition string `json:"definition,omitempty"`
	Quality    string `json:"quality,omitempty"`
	Codec      string `json:"codec,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	// Bitrate 单位 bit/s。
	Bitrate int64 `json:"bitrate,omitempty"`
	HLS     bool  `json:"hls,omitempty"`
	Dash    bool  `json:"dash,omitempty"`
	// Protected 标记该清晰度是加密的。
	Protected bool `json:"protected"`
	// Encryption 是加密方案，如 "cenc-aes-ctr"。
	Encryption string `json:"encryption,omitempty"`
}

// PlaybackInfo 是一个播放模型的**可公开摘要**，用于诊断页与 cmd/probe。
//
// 只放可以出现在日志、界面、诊断包里的信息：清晰度标签、codec、是否加密、
// 有效期。密钥值与密钥材料永不进入本结构（I2）；KeyInMemory 只是一个布尔标记，
// 说明本会话是否持有该集的内存密钥（方案 §5.3 has_key 判据在 DTO 侧的对应物）。
type PlaybackInfo struct {
	EpisodeID   string `json:"episodeId"`
	VideoID     string `json:"videoId,omitempty"`
	MediaType   string `json:"mediaType,omitempty"`
	DurationSec int    `json:"durationSec,omitempty"`
	Protected   bool   `json:"protected"`
	// ExpiresAt 是 CDN 地址失效时间（unix 秒，方案 F15）。0 表示服务端没给。
	ExpiresAt int64 `json:"expiresAt,omitempty"`
	// HasAudio 为真表示模型带独立音轨（pb.AudioURL 非空，走 B1 时须额外 map 音轨）。
	HasAudio bool `json:"hasAudio"`
	// KeyInMemory 只说明本会话内存里是否有该集的内容密钥，不含密钥本身。
	KeyInMemory bool         `json:"keyInMemory"`
	Streams     []StreamInfo `json:"streams"`
}
