.PHONY: fonts-clean

all: clockdrawer/fonts/Aileron-Regular.otf
	rm -rf bin
	GOOS=darwin GOARCH=arm64 go build -o bin/darwin-arm64/imageclock
	GOOS=linux GOARCH=arm64 go build -o bin/linux-arm64/imageclock
	GOOS=linux GOARCH=amd64 go build -o bin/linux-amd64/imageclock

clockdrawer/fonts/Aileron-Regular.otf:
	mkdir -p ./clockdrawer/fonts ./clockdrawer/fonts-tmp
	wget https://www.fontsquirrel.com/fonts/download/aileron -O clockdrawer/fonts-tmp/aileron
	unzip clockdrawer/fonts-tmp/aileron -d clockdrawer/fonts-tmp
	cp clockdrawer/fonts-tmp/Aileron-Regular.otf  'clockdrawer/fonts-tmp/CC0 1.0 Universal (CC0 1.0)  Public Domain Dedication.txt' clockdrawer/fonts
	rm -rf clockdrawer/fonts-tmp


fonts-clean:
	rm -rf clockdrawer/fonts clockdrawer/fonts-tmp
