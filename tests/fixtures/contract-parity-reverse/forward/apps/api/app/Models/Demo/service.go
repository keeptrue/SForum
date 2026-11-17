package demo

import (
	"context"
	"errors"
)

// ErrDenied 是 fixture 的业务哨兵：由领域方法直接 return，再经控制器映射器翻成 403。
var ErrDenied = errors.New("demo.denied")

type Service struct{}

// Do 直接 return 哨兵。门靠「handler 调用的 service 方法直接 return 哨兵」把 403 归因到路由。
func (s *Service) Do(_ context.Context) (string, error) {
	return "", ErrDenied
}
