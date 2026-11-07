//go:build !linux

package metrics

func diskKB(string) (total, free int64) { return 0, 0 }
