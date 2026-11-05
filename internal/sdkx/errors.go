package sdkx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/beilunyang/openhongguo/sdk"
)

// 哨兵错误原样再导出：上层用 sdkx.ErrXxx 做 errors.Is，不必 import sdk。
//
// 注意 ErrAlbumUnavailable 在 SDK 里**没有生产者**（方案 M3），不得作为分支条件；
// 匿名读不到合集时表现为 ErrContentUnavailable。
var (
	ErrInvalidOptions         = sdk.ErrInvalidOptions
	ErrProfileNotFound        = sdk.ErrProfileNotFound
	ErrSignerUnavailable      = sdk.ErrSignerUnavailable
	ErrAuthenticationRequired = sdk.ErrAuthenticationRequired
	ErrDRMProtected           = sdk.ErrDRMProtected
	ErrNotProtected           = sdk.ErrNotProtected
	ErrAnonymousLimited       = sdk.ErrAnonymousLimited
	ErrRateLimited            = sdk.ErrRateLimited
	ErrTransport              = sdk.ErrTransport
	ErrSignatureRejected      = sdk.ErrSignatureRejected
	ErrContentUnavailable     = sdk.ErrContentUnavailable
	ErrParamInvalid           = sdk.ErrParamInvalid
	ErrChallengeRequired      = sdk.ErrChallengeRequired
	ErrDeviceRejected         = sdk.ErrDeviceRejected
	ErrEndOfList              = sdk.ErrEndOfList
)

// 错误归一后的稳定代码。前端只认这些字符串，不认 SDK 的哨兵值。
const (
	CodeOK                 = "ok"
	CodeAnonymousLimited   = "anonymous_limited"
	CodeDeviceRejected     = "device_rejected"
	CodeRateLimited        = "rate_limited"
	CodeContentUnavailable = "content_unavailable"
	CodeAuthRequired       = "auth_required"
	CodeDRMProtected       = "drm_protected"
	CodeNotProtected       = "not_protected"
	CodeTransport          = "transport"
	CodeSignatureRejected  = "signature_rejected"
	CodeParamInvalid       = "param_invalid"
	CodeChallengeRequired  = "challenge_required"
	CodeEndOfList          = "end_of_list"
	CodeInvalidOptions     = "invalid_options"
	CodeCanceled           = "canceled"
	CodeUnknown            = "unknown"
)

// Classify 把一个错误归一成 (机器码, 用户可读文案, 是否值得原样重试)。
//
// 码与文案按方案 §4.6 / §6.3 / §7.7 的口径定：例如 ErrAnonymousLimited 在列表
// 接口只表示「该剧暂无可播放分集」，在播放接口才可能是额度耗尽（F8），
// 所以文案只说「暂无可播放数据」，把配额判断留给设备池的 Status()。
//
// Classify(nil) 返回 (CodeOK, "", false)。
func Classify(err error) (code string, userMsg string, retryable bool) {
	if err == nil {
		return CodeOK, "", false
	}
	retryable = sdk.Retryable(err)
	switch {
	case errors.Is(err, ErrAnonymousLimited):
		return CodeAnonymousLimited, "匿名请求返回空结果：该内容暂无可播放数据，或该设备的额度已用尽", retryable
	case errors.Is(err, ErrDeviceRejected):
		return CodeDeviceRejected, "匿名设备被服务端拒绝，正在更换设备", retryable
	case errors.Is(err, ErrRateLimited):
		return CodeRateLimited, "请求过于频繁，请稍后重试", retryable
	case errors.Is(err, ErrContentUnavailable):
		return CodeContentUnavailable, "该内容已下架或暂不可用", retryable
	case errors.Is(err, ErrAuthenticationRequired):
		return CodeAuthRequired, "该内容需要登录，匿名模式无法播放", retryable
	case errors.Is(err, ErrChallengeRequired):
		return CodeChallengeRequired, "服务端要求验证码，匿名模式无法继续", retryable
	case errors.Is(err, ErrDRMProtected):
		return CodeDRMProtected, "加密内容（正常状态，需本地取密钥，不是错误）", retryable
	case errors.Is(err, ErrNotProtected):
		return CodeNotProtected, "该分集未加密，无需密钥", retryable
	case errors.Is(err, ErrTransport):
		return CodeTransport, "网络请求失败，请检查网络后重试", retryable
	case errors.Is(err, ErrSignatureRejected):
		return CodeSignatureRejected, "请求签名被拒绝，可能是 SDK 版本落后", retryable
	case errors.Is(err, ErrParamInvalid):
		return CodeParamInvalid, "请求参数无效", retryable
	case errors.Is(err, ErrEndOfList):
		return CodeEndOfList, "没有更多内容了", retryable
	case errors.Is(err, ErrInvalidOptions):
		return CodeInvalidOptions, "客户端配置无效", retryable
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return CodeCanceled, "操作已取消", retryable
	}
	return CodeUnknown, "未知错误", retryable
}

// RetryAfter 返回重试 err 前应等多久：限流响应带服务端封禁窗口时用窗口，否则用 fallback。
func RetryAfter(err error, fallback time.Duration) time.Duration {
	return sdk.RetryAfter(err, fallback)
}

// IsRetryable 报告原样重发同一请求是否可能成功（方案 F16）。
func IsRetryable(err error) bool { return sdk.Retryable(err) }

// KeyUnavailableError 表示播放模型是加密的，但密钥材料无法解包。
//
// 对应方案 §6.3 的「该集加密方式暂不支持」：模型本身可用（pb != nil），
// 只是拿不到内容密钥，**不得**当作可播。Unwrap 保留底层原因便于排障，
// 但底层原因里只有包装材料，不含密钥本身。
type KeyUnavailableError struct {
	EpisodeID string
	Err       error
}

func (e *KeyUnavailableError) Error() string {
	return fmt.Sprintf("sdkx: content key unavailable for episode %s: %v", e.EpisodeID, e.Err)
}

func (e *KeyUnavailableError) Unwrap() error { return e.Err }
