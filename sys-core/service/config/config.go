package config

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"math/big"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/caddyserver/certmagic"
	"github.com/karrick/godirwalk"
	"github.com/segmentio/encoding/json"
	"github.com/segmentio/ksuid"
	"google.golang.org/grpc/credentials"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gopkg.in/yaml.v2"
)

// TODO @gutterbacon: use this as abstraction for per module configs.
type Configurator interface {
	Validate() error
}

func LoadFile(filepath string) ([]byte, error) {
	return ioutil.ReadFile(filepath)
}

func GenRandomByteSlice(length int) ([]byte, error) {
	const legalChars = "01234567890ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz-_"
	rnd := make([]byte, length)
	for i := 0; i < length; i++ {
		num, err := rand.Int(rand.Reader, big.NewInt(int64(len(legalChars))))
		if err != nil {
			return nil, err
		}
		rnd = append(rnd, legalChars[num.Int64()])
	}
	return rnd, nil
}

// FileExists checks if a file exists and is not a directory before we
// try using it to prevent further errors.
func FileExists(filename string) bool {
	info, err := os.Stat(filename)
	if os.IsNotExist(err) {
		return false
	}
	return !info.IsDir()
}

// PathExists checks if a directory exists.
func PathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func LoadTLSKeypair(certPath, keyPath string) (credentials.TransportCredentials, error) {
	serverCert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}

	config := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.NoClientCert,
	}

	return credentials.NewTLS(config), nil
}

func ClientLoadCA(cacertPath string) (credentials.TransportCredentials, error) {
	pemServerCA, err := ioutil.ReadFile(cacertPath)
	if err != nil {
		return nil, err
	}
	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(pemServerCA) {
		return nil, fmt.Errorf("failed to add server CA's certificate")
	}
	config := &tls.Config{
		RootCAs: certPool,
	}
	return credentials.NewTLS(config), nil
}

func GenerateTLSConfig(staging bool, email string, domains ...string) (*tls.Config, error) {
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) {
			return &certmagic.Default, nil
		},
	})
	acmeMgr := certmagic.ACMEManager{
		Agreed: true,
		Email:  email,
	}
	cfg := certmagic.Default
	if staging {
		acmeMgr.CA = certmagic.LetsEncryptStagingCA
	} else {
		acmeMgr.CA = certmagic.LetsEncryptProductionCA
	}
	myAcme := certmagic.NewACMEManager(&cfg, acmeMgr)
	magic := certmagic.New(cache, cfg)
	magic.Issuer = myAcme
	err := magic.ManageSync(domains)
	if err != nil {
		return nil, err
	}
	return magic.TLSConfig(), nil
}

func NewID() string {
	return ksuid.New().String()
}

// helper func to convert unix timestamp (seconds) to timestamppb.Timestamp
func UnixToUtcTS(in int64) *timestamppb.Timestamp {
	t := time.Unix(0, in).UTC()
	return timestamppb.New(t)
}

func IsDirectory(path string) (bool, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return fileInfo.IsDir(), err
}

func CurrentTimestamp() int64 {
	return time.Now().UTC().UnixNano()
}

func MarshalPretty(any interface{}) ([]byte, error) {
	return json.MarshalIndent(&any, "", "  ")
}

func MarshalYAML(any interface{}) ([]byte, error) {
	return yaml.Marshal(&any)
}

func UnmarshalJson(data []byte, any interface{}) error {
	return json.Unmarshal(data, any)
}

func UnmarshalYAML(data []byte, any interface{}) error {
	return yaml.UnmarshalStrict(data, any)
}

func ListFiles(dirpath string) ([]os.FileInfo, error) {
	files, err := ioutil.ReadDir(dirpath)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].Name() < files[j].Name()
	})
	return files, nil
}

func TsToUnixUTC(in *timestamppb.Timestamp) int64 {
	return int64(in.GetNanos())
}

func DecodeB64(in string) ([]byte, error) {
	return base64.RawStdEncoding.DecodeString(in)
}

func getCACert(domain string) (*x509.Certificate, error) {
	conf := &tls.Config{
		InsecureSkipVerify: true,
	}
	conn, err := tls.Dial("tcp", fmt.Sprintf("%s:443", domain), conf)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	certs := conn.ConnectionState().PeerCertificates
	for _, cert := range certs {
		if cert.IsCA {
			return cert, nil
		}
	}
	return nil, fmt.Errorf("unable to get CA from server: cert does not exist")
}

func DownloadCACert(outputPath, domain string) error {
	cert, err := getCACert(domain)
	if err != nil {
		return err
	}
	publicKeyBlock := pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	}
	publicKeyPem := pem.EncodeToMemory(&publicKeyBlock)
	return ioutil.WriteFile(outputPath, publicKeyPem, 0644)
}

func DedupSlice(stringSlice []string) []string {
	keys := make(map[string]bool)
	list := []string{}
	for _, entry := range stringSlice {
		if _, value := keys[entry]; !value {
			keys[entry] = true
			list = append(list, entry)
		}
	}
	return list
}

// LookupFile in a directory, if it exists, returns the full path to the file
// if it doesn't returns error
// takes the directory of the file, and the string containing parts of the file (or the whole file name)
func LookupFile(dirname, contains string) (string, error) {
	var matched string
	_ = godirwalk.Walk(dirname, &godirwalk.Options{
		FollowSymbolicLinks: true,
		Callback: func(osPathname string, de *godirwalk.Dirent) error {
			if de.IsRegular() && strings.Contains(de.Name(), contains) {
				matched = osPathname
				return nil
			}
			return nil
		},
		Unsorted: true, // (optional) set true for faster yet non-deterministic enumeration (see godoc)
	})
	if matched == "" {
		return "", fmt.Errorf("file contains %s not found", contains)
	}

	return matched, nil
}
