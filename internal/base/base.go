package base

func GoWrap(goFunc func()) {
	go func() {
		defer func() {
			recover() // 捕获 panic，防止协程崩溃
		}()
		goFunc()
	}()
}
