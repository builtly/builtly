package storage

// SetTestEndpoint forces R2 endpoint for tests (fake S3 server).
func SetTestEndpoint(svc *Service, endpoint string) {
	svc.testEndpoint = endpoint
}
