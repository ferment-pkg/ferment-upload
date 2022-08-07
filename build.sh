echo "Building For Linux ARM64"
GOOS=linux GOARCH=arm64 go build -o build/linux/aarch64/server -ldflags="-s -w" main.go
echo "Building For Linux x86_64"
GOOS=linux GOARCH=amd64 go build -o build/linux/x86_64/server -ldflags="-s -w" main.go
echo "Building For MacOS Universal"
GOOS=darwin GOARCH=amd64 go build -o build/macos/universal/server-amd64 -ldflags="-s -w" main.go
GOOS=darwin GOARCH=arm64 go build -o build/macos/universal/server-arm64 -ldflags="-s -w" main.go
lipo -create build/macos/universal/server-amd64 build/macos/universal/server-arm64 -output build/macos/universal/server
rm build/macos/universal/server-amd64 build/macos/universal/server-arm64
echo "Done"
