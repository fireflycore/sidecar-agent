package lb

import "sync/atomic"

// RoundRobin 是 V3 当前阶段最小的轮询负载均衡器。
//
// 它只做一件事：
// - 在候选实例列表中按顺序轮询选择一个实例
type RoundRobin struct {
	// counter 用于记录当前轮询游标。
	counter uint64
}

// NewRoundRobin 创建一个最小轮询器。
func NewRoundRobin() *RoundRobin {
	return &RoundRobin{}
}

// Pick 从实例列表中挑选一个目标地址。
//
// 当实例列表为空时，返回空字符串，由上层决定如何报错。
func (r *RoundRobin) Pick(instances []string) string {
	if len(instances) == 0 {
		return ""
	}

	// 这里用原子递增保证并发场景下也能稳定轮询。
	index := atomic.AddUint64(&r.counter, 1) - 1
	return instances[index%uint64(len(instances))]
}
