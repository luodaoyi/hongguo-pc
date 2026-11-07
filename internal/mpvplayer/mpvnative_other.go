//go:build !windows

// 非 Windows：mpv 内核暂不提供（CI 上 linux/darwin 不分发 libmpv）。
// 接口实现全部返回不可用，应用探测后自动回退 <video>。
package mpvplayer

import "fmt"

func nativeAvailable() bool { return false }

type nativeClient struct{}

func newNativeClient() (*nativeClient, error) {
	return nil, fmt.Errorf("mpv 内核仅支持 Windows")
}

func (c *nativeClient) SetOptionString(name, value string) error {
	return fmt.Errorf("mpv 不可用")
}

func (c *nativeClient) SetPropertyString(name, value string) error {
	return fmt.Errorf("mpv 不可用")
}

func (c *nativeClient) GetProperty(name string, format int) (any, error) {
	return nil, fmt.Errorf("mpv 不可用")
}

func (c *nativeClient) Command(args []string) error {
	return fmt.Errorf("mpv 不可用")
}

func (c *nativeClient) WaitEvent(timeout float64) mpvEvent {
	return mpvEvent{ID: eventNone}
}

func (c *nativeClient) TerminateDestroy() {}

func (c *nativeClient) Initialize() error {
	return fmt.Errorf("mpv 不可用")
}
