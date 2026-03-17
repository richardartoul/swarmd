package common

type PaginationWindow struct {
	Start int
	Limit int
}

func NormalizePaginationWindow(offset, limit, defaultLimit, maxLimit int) PaginationWindow {
	if offset <= 0 {
		offset = 1
	}
	return PaginationWindow{
		Start: offset - 1,
		Limit: ClampInt(limit, defaultLimit, maxLimit),
	}
}

func (w PaginationWindow) RangeForTotal(total int) (int, int, bool) {
	if w.Start >= total {
		return total, total, false
	}
	end := MinInt(total, w.Start+w.Limit)
	return w.Start, end, true
}
