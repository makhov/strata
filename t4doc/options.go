package t4doc

const defaultMaxScan = int64(10_000)

type findOptions struct {
	limit     int
	allowScan bool
	maxScan   int64
}

// FindOption configures Find and FindOne.
type FindOption func(*findOptions)

// Limit caps the number of returned documents. A non-positive value means no
// result limit.
func Limit(n int) FindOption {
	return func(o *findOptions) { o.limit = n }
}

// AllowScan permits a full collection scan when no usable index exists.
func AllowScan() FindOption {
	return func(o *findOptions) { o.allowScan = true }
}

// MaxScan caps the number of documents read during an AllowScan collection
// scan. Values <= 0 use the default cap.
func MaxScan(n int64) FindOption {
	return func(o *findOptions) {
		if n > 0 {
			o.maxScan = n
		}
	}
}

func collectFindOptions(opts []FindOption) findOptions {
	o := findOptions{maxScan: defaultMaxScan}
	for _, fn := range opts {
		fn(&o)
	}
	if o.maxScan <= 0 {
		o.maxScan = defaultMaxScan
	}
	return o
}
