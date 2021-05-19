package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path"
	"strings"

	minio "github.com/minio/minio-go/v7"
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
	path := path.Clean(name)

	// used to serve "/"
	isDirectory := strings.HasSuffix(name, pathSeparator)
	if isDirectory {
		return &httpMinioObject{
			client: s3.Client,
			object: nil,
			isDir:  true,
			bucket: bucket,
			prefix: strings.TrimSuffix(name, pathSeparator),
		}, nil
	}

	path = strings.TrimPrefix(path, pathSeparator)
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
	paths := [3]string{
		path.Join(s3.options.root, name),
		path.Join(s3.options.root, name, "index.html"),
		path.Join(s3.options.root, "404.html"),
	}
	for _, path := range paths {
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
