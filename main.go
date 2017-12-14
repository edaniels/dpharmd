package main

import (
	"bytes"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

var secret = flag.String("secret", "", "request secret")
var testMu sync.Mutex

func main() {
	flag.Parse()

	if *secret == "" {
		flag.Usage()
		os.Exit(1)
	}

	if err := http.ListenAndServe(":8080", http.HandlerFunc(handler)); err != nil {
		log.Fatal(err)
	}
}

func handler(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	if auth != *secret {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}
	switch query.Get("test_type") {
	case testTypeAndroid:
		runAndroidTest(w, r, query)
		return
	case testTypeIOS:
		runIOSTest(w, r, query)
		return
	}

	w.WriteHeader(http.StatusBadRequest)
	w.Write([]byte("must specify 'test_type'"))
}

func runAndroidTest(w http.ResponseWriter, r *http.Request, query url.Values) {
	testPackage := query.Get("test_package")
	if testPackage == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("must specify 'test_package'"))
		return
	}

	dir := os.TempDir()
	apkFileName := filepath.Join(dir, fmt.Sprintf("test_%s.apk", RandomAlphaNumericString(5)))
	apkFile, err := os.OpenFile(apkFileName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}
	defer os.Remove(apkFile.Name())

	reader := io.TeeReader(r.Body, apkFile)
	if _, err := ioutil.ReadAll(reader); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}
	apkFile.Close()

	log.Printf("android: Installing and running %s", testPackage)

	testMu.Lock()
	defer testMu.Unlock()

	cmd := exec.Command("adb", "install", "-r", apkFile.Name())
	result, err := cmd.CombinedOutput()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		w.Write([]byte("\n"))
		w.Write(result)
		return
	}

	testPackageWithRunner := fmt.Sprintf("%s/android.support.test.runner.AndroidJUnitRunner", testPackage)
	cmd = exec.Command("adb", "shell", "am", "instrument", "-w", testPackageWithRunner)
	result, err = cmd.CombinedOutput()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		w.Write([]byte("\n"))
		w.Write(result)
		return
	}

	w.Write(result)
}

func runIOSTest(w http.ResponseWriter, r *http.Request, query url.Values) {
	testDestination := query.Get("test_destination")
	if testDestination == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("must specify 'test_destination'"))
		return
	}

	testSchemesStr := query.Get("test_schemes")
	if testSchemesStr == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("must specify 'test_schemes'"))
		return
	}
	testSchemes := strings.Split(testSchemesStr, ",")

	dir := os.TempDir()
	if err := os.Chdir(dir); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	sourceDir := RandomAlphaNumericString(5)
	if err := os.Mkdir(sourceDir, 0755); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	defer os.RemoveAll(sourceDir)

	if err := os.Chdir(filepath.Join(dir, sourceDir)); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}

	sourceFileName := filepath.Join(dir, sourceDir, "source.tgz")
	sourceFile, err := os.OpenFile(sourceFileName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}

	reader := io.TeeReader(r.Body, sourceFile)
	if _, err := ioutil.ReadAll(reader); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}
	sourceFile.Close()

	log.Print("ios: Unpacking source")

	cmd := exec.Command("tar", "xf", sourceFile.Name())
	result, err := cmd.CombinedOutput()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		w.Write([]byte("\n"))
		w.Write(result)
		return
	}

	testMu.Lock()
	defer testMu.Unlock()

	var testFailed bool
	for _, scheme := range testSchemes {
		testStartMsg := fmt.Sprintf("ios: Testing scheme %q on destination %q", scheme, testDestination)
		log.Print(testStartMsg)
		w.Write([]byte(testStartMsg))
		w.Write([]byte("\n"))
		cmd = exec.Command("xcodebuild", "test", "-destination", testDestination, "-scheme", scheme)
		result, err = cmd.CombinedOutput()
		if err != nil {
			testFailed = true
			w.Write([]byte("!!TEST FAILED!!\n"))
			w.Write([]byte(err.Error()))
			w.Write([]byte("\n"))
			w.Write(result)
			continue
		}

		w.Write([]byte("!!TEST PASSED!!\n"))
		w.Write(result)
	}

	if !testFailed {
		w.Write([]byte("!!ALL TESTS PASSED!!\n"))
	}
}

const (
	testTypeAndroid = "android"
	testTypeIOS     = "ios"
)

// RandomAlphaNumericString generates a new random alphanumeric key
func RandomAlphaNumericString(length int) string {
	return randomStringFromAlphabet(stringAlphaNumeric, length)
}

var (
	stringAlpha        = []rune("abcdefghijklmnopqrstuvwxyz")
	stringAlphaUpper   = []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	stringNumeric      = []rune("1234567890")
	stringSpecial      = []rune("!@#$%^&*()_-")
	stringAlphaNumeric = append(append(append([]rune{}, stringAlpha...), stringAlphaUpper...), stringNumeric...)
)

func randomStringFromAlphabet(alpha []rune, length int) string {
	var buf bytes.Buffer

	for i := 0; i < length; i++ {
		val, err := rand.Int(rand.Reader, big.NewInt(int64(len(alpha))))
		if err != nil {
			panic(err)
		}
		buf.WriteRune(alpha[val.Int64()])
	}

	return buf.String()
}
