package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/caddyserver/certmagic"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/s3utils"
)

type S3Options struct {
	root string
}

// S3 - A S3 implements FileSystem using the minio client
// allowing access to your S3 buckets and objects.
//
// Note that S3 will allow all access to files in your private
// buckets, If you have any sensitive information please make
// sure to not sure this project.
type S3 struct {
	*minio.Client
	bucket  string
	options S3Options
}

// Open - implements http.Filesystem implementation.
func (s3 *S3) Open(name string) (http.File, error) {
	// isDirectory := strings.HasSuffix(name, pathSeparator)
	path := path.Join(s3.options.root, name)

	// if isDirectory {
	// 	return &httpMinioObject{
	// 		client: s3.Client,
	// 		object: nil,
	// 		isDir:  true,
	// 		bucket: bucket,
	// 		prefix: path,
	// 	}, nil
	// }

	obj, err := getObject(context.Background(), s3, path)
	if err != nil {
		return nil, os.ErrNotExist
	}

	return &httpMinioObject{
		client: s3.Client,
		object: obj,
		isDir:  false,
		bucket: bucket,
		prefix: path,
	}, nil
}

func getObject(ctx context.Context, s3 *S3, name string) (*minio.Object, error) {
	paths := [3]string{name, path.Join(name, "index.html"), "/404.html"}
	for _, path := range paths {
		log.Println("try:", path)
		obj, err := s3.Client.GetObject(ctx, s3.bucket, path, minio.GetObjectOptions{})
		if err != nil {
			log.Println(err)
			continue
		}

		_, err = obj.Stat()
		if err != nil {
			// do not log "file" in bucket not found errors
			if minio.ToErrorResponse(err).Code != "NoSuchKey" {
				log.Println(err)
			}
			continue
		}

		return obj, nil
	}

	return nil, os.ErrNotExist
}

var (
	endpoint    string
	accessKey   string
	secretKey   string
	address     string
	bucket      string
	tlsCert     string
	tlsKey      string
	letsEncrypt bool
	rootPath    string
	useCache    bool
	cacheTTL    int
)

func init() {
	flag.StringVar(&endpoint, "endpoint", "", "S3 server endpoint")
	flag.StringVar(&accessKey, "accessKey", "", "Access key of S3 storage")
	flag.StringVar(&secretKey, "secretKey", "", "Secret key of S3 storage")
	flag.StringVar(&bucket, "bucket", "", "Bucket name which hosts static files")
	flag.StringVar(&address, "address", "127.0.0.1:8080", "Bind to a specific ADDRESS:PORT, ADDRESS can be an IP or hostname")
	flag.StringVar(&tlsCert, "ssl-cert", "", "TLS certificate for this server")
	flag.StringVar(&tlsKey, "ssl-key", "", "TLS private key for this server")
	flag.BoolVar(&letsEncrypt, "lets-encrypt", false, "Enable Let's Encrypt")
	flag.StringVar(&rootPath, "root", "", "Serve files from a sub folder of the bucket")
	flag.BoolVar(&useCache, "use-cache", false, "Enable caching of http responses (default: disabled)")
	flag.IntVar(&cacheTTL, "cache-ttl", 300, "TTL of items in cache (default: 300 seconds)")
}

// NewCustomHTTPTransport returns a new http configuration
// used while communicating with the cloud backends.
// This sets the value for MaxIdleConnsPerHost from 2 (go default)
// to 100.
func NewCustomHTTPTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   1024,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
	}
}

func main() {
	flag.Parse()

	if strings.TrimSpace(bucket) == "" {
		log.Fatalln(`Bucket name cannot be empty, please provide 's3www -bucket "mybucket"'`)
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		log.Fatalln(err)
	}

	// Chains all credential types, in the following order:
	//  - AWS env vars (i.e. AWS_ACCESS_KEY_ID)
	//  - AWS creds file (i.e. AWS_SHARED_CREDENTIALS_FILE or ~/.aws/credentials)
	//  - IAM profile based credentials. (performs an HTTP
	//    call to a pre-defined endpoint, only valid inside
	//    configured ec2 instances)
	var defaultAWSCredProviders = []credentials.Provider{
		&credentials.EnvAWS{},
		&credentials.FileAWSCredentials{},
		&credentials.IAM{
			Client: &http.Client{
				Transport: NewCustomHTTPTransport(),
			},
		},
		&credentials.EnvMinio{},
	}
	if accessKey != "" && secretKey != "" {
		defaultAWSCredProviders = []credentials.Provider{
			&credentials.Static{
				Value: credentials.Value{
					AccessKeyID:     accessKey,
					SecretAccessKey: secretKey,
				},
			},
		}
	}

	// If we see an Amazon S3 endpoint, then we use more ways to fetch backend credentials.
	// Specifically IAM style rotating credentials are only supported with AWS S3 endpoint.
	creds := credentials.NewChainCredentials(defaultAWSCredProviders)

	client, err := minio.New(u.Host, &minio.Options{
		Creds:        creds,
		Secure:       u.Scheme == "https",
		Region:       s3utils.GetRegionFromURL(*u),
		BucketLookup: minio.BucketLookupAuto,
		Transport:    NewCustomHTTPTransport(),
	})
	if err != nil {
		log.Fatalln(err)
	}

	s3Options := S3Options{
		rootPath,
	}
	mux := http.FileServer(&S3{client, bucket, s3Options})

	if useCache {
		log.Printf("Using cache for http request (ttl: %d seconds)\n", cacheTTL)
		mux = cacheHandle(mux, cacheTTL)
	}

	if letsEncrypt {
		log.Printf("Started listening on https://%s\n", address)
		certmagic.HTTPS([]string{address}, mux)
	} else if tlsCert != "" && tlsKey != "" {
		log.Printf("Started listening on https://%s\n", address)
		log.Fatalln(http.ListenAndServeTLS(address, tlsCert, tlsKey, mux))
	} else {
		log.Printf("Started listening on http://%s\n", address)
		log.Fatalln(http.ListenAndServe(address, mux))
	}
}
