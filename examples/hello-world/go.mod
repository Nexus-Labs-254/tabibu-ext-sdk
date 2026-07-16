module hello-world

go 1.24

require github.com/tabibumrs/tabibu-ext-sdk v0.0.0

require (
	github.com/BryanMwangi/pine v1.1.8 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/joho/godotenv v1.5.1 // indirect
)

// Remove this replace directive once the SDK is published to GitHub.
replace github.com/tabibumrs/tabibu-ext-sdk => ../../
