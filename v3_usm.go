// Copyright 2012 The GoSNMP Authors. All rights reserved.  Use of this
// source code is governed by a BSD-style license that can be found in the
// LICENSE file.

// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gosnmp

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/hmac"
	"crypto/md5"
	crand "crypto/rand"
	"crypto/sha1"
	_ "crypto/sha256" // Register hash function #4 (SHA224), #5 (SHA256)
	_ "crypto/sha512" // Register hash function #6 (SHA384), #7 (SHA512)
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"strings"
	"sync"
	"sync/atomic"
)

// SnmpV3AuthProtocol describes the authentication protocol in use by an authenticated SnmpV3 connection.
type SnmpV3AuthProtocol uint8

// NoAuth, MD5, and SHA are implemented
const (
	NoAuth SnmpV3AuthProtocol = 1
	MD5    SnmpV3AuthProtocol = 2
	SHA    SnmpV3AuthProtocol = 3
	SHA224 SnmpV3AuthProtocol = 4
	SHA256 SnmpV3AuthProtocol = 5
	SHA384 SnmpV3AuthProtocol = 6
	SHA512 SnmpV3AuthProtocol = 7
)

//go:generate stringer -type=SnmpV3AuthProtocol

// HashType maps the AuthProtocol's hash type to an actual crypto.Hash object.
func (authProtocol SnmpV3AuthProtocol) HashType() crypto.Hash {
	switch authProtocol {
	default:
		return crypto.MD5
	case SHA:
		return crypto.SHA1
	case SHA224:
		return crypto.SHA224
	case SHA256:
		return crypto.SHA256
	case SHA384:
		return crypto.SHA384
	case SHA512:
		return crypto.SHA512
	}
}

//nolint:gochecknoglobals
var macVarbinds = [][]byte{
	{},                     // dummy
	{byte(OctetString), 0}, // NoAuth
	{byte(OctetString), 12, // MD5
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0},
	{byte(OctetString), 12, // SHA
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0},
	{byte(OctetString), 16, // SHA224
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0},
	{byte(OctetString), 24, // SHA256
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0},
	{byte(OctetString), 32, // SHA384
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0},
	{byte(OctetString), 48, // SHA512
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0,
		0, 0, 0, 0}}

// SnmpV3PrivProtocol is the privacy protocol in use by an private SnmpV3 connection.
type SnmpV3PrivProtocol uint8

// NoPriv, DES implemented, AES planned
// Changed: AES192, AES256, AES192C, AES256C added
const (
	NoPriv  SnmpV3PrivProtocol = 1
	DES     SnmpV3PrivProtocol = 2
	AES     SnmpV3PrivProtocol = 3
	AES192  SnmpV3PrivProtocol = 4 // Blumenthal-AES192
	AES256  SnmpV3PrivProtocol = 5 // Blumenthal-AES256
	AES192C SnmpV3PrivProtocol = 6 // Reeder-AES192
	AES256C SnmpV3PrivProtocol = 7 // Reeder-AES256
)

//go:generate stringer -type=SnmpV3PrivProtocol

// UsmSecurityParameters is an implementation of SnmpV3SecurityParameters for the UserSecurityModel
type UsmSecurityParameters struct {
	mu sync.Mutex
	// localAESSalt must be 64bit aligned to use with atomic operations.
	localAESSalt uint64
	localDESSalt uint32

	AuthoritativeEngineID    string
	AuthoritativeEngineBoots uint32
	AuthoritativeEngineTime  uint32
	UserName                 string
	AuthenticationParameters string
	PrivacyParameters        []byte

	AuthenticationProtocol SnmpV3AuthProtocol
	PrivacyProtocol        SnmpV3PrivProtocol

	AuthenticationPassphrase string
	PrivacyPassphrase        string

	SecretKey  []byte
	PrivacyKey []byte

	Logger Logger
}

func (sp *UsmSecurityParameters) getIdentifier() string {
	return sp.UserName
}

func (sp *UsmSecurityParameters) getLogger() Logger {
	return sp.Logger
}

func (sp *UsmSecurityParameters) setLogger(log Logger) {
	sp.Logger = log
}

// Description logs authentication paramater information to the provided GoSNMP Logger
func (sp *UsmSecurityParameters) Description() string {
	var sb strings.Builder
	sb.WriteString("user=")
	sb.WriteString(sp.UserName)

	sb.WriteString(",engine=(")
	sb.WriteString(hex.EncodeToString([]byte(sp.AuthoritativeEngineID)))
	// sb.WriteString(sp.AuthoritativeEngineID)
	sb.WriteString(")")

	switch sp.AuthenticationProtocol {
	case NoAuth:
		sb.WriteString(",auth=noauth")
	case MD5:
		sb.WriteString(",auth=md5")
	case SHA:
		sb.WriteString(",auth=sha")
	case SHA224:
		sb.WriteString(",auth=sha224")
	case SHA256:
		sb.WriteString(",auth=sha256")
	case SHA384:
		sb.WriteString(",auth=sha384")
	case SHA512:
		sb.WriteString(",auth=sha512")
	}
	sb.WriteString(",authPass=")
	sb.WriteString(sp.AuthenticationPassphrase)

	switch sp.PrivacyProtocol {
	case NoPriv:
		sb.WriteString(",priv=NoPriv")
	case DES:
		sb.WriteString(",priv=DES")
	case AES:
		sb.WriteString(",priv=AES")
	case AES192:
		sb.WriteString(",priv=AES192")
	case AES256:
		sb.WriteString(",priv=AES256")
	case AES192C:
		sb.WriteString(",priv=AES192C")
	case AES256C:
		sb.WriteString(",priv=AES256C")
	}
	sb.WriteString(",privPass=")
	sb.WriteString(sp.PrivacyPassphrase)

	return sb.String()
}

// SafeString returns a logging safe (no secrets) string of the UsmSecurityParameters
func (sp *UsmSecurityParameters) SafeString() string {
	return fmt.Sprintf("AuthoritativeEngineID:%s, AuthoritativeEngineBoots:%d, AuthoritativeEngineTimes:%d, UserName:%s, AuthenticationParameters:%s, PrivacyParameters:%v, AuthenticationProtocol:%s, PrivacyProtocol:%s",
		sp.AuthoritativeEngineID,
		sp.AuthoritativeEngineBoots,
		sp.AuthoritativeEngineTime,
		sp.UserName,
		sp.AuthenticationParameters,
		sp.PrivacyParameters,
		sp.AuthenticationProtocol,
		sp.PrivacyProtocol,
	)
}

// Log logs security paramater information to the provided GoSNMP Logger
func (sp *UsmSecurityParameters) Log() {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.Logger.Printf("SECURITY PARAMETERS:%s", sp.SafeString())
}

// Copy method for UsmSecurityParameters used to copy a SnmpV3SecurityParameters without knowing it's implementation
func (sp *UsmSecurityParameters) Copy() SnmpV3SecurityParameters {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return &UsmSecurityParameters{AuthoritativeEngineID: sp.AuthoritativeEngineID,
		AuthoritativeEngineBoots: sp.AuthoritativeEngineBoots,
		AuthoritativeEngineTime:  sp.AuthoritativeEngineTime,
		UserName:                 sp.UserName,
		AuthenticationParameters: sp.AuthenticationParameters,
		PrivacyParameters:        sp.PrivacyParameters,
		AuthenticationProtocol:   sp.AuthenticationProtocol,
		PrivacyProtocol:          sp.PrivacyProtocol,
		AuthenticationPassphrase: sp.AuthenticationPassphrase,
		PrivacyPassphrase:        sp.PrivacyPassphrase,
		SecretKey:                sp.SecretKey,
		PrivacyKey:               sp.PrivacyKey,
		localDESSalt:             sp.localDESSalt,
		localAESSalt:             sp.localAESSalt,
		Logger:                   sp.Logger,
	}
}

func (sp *UsmSecurityParameters) getDefaultContextEngineID() string {
	return sp.AuthoritativeEngineID
}

// InitSecurityKeys initializes the Priv and Auth keys if needed
func (sp *UsmSecurityParameters) InitSecurityKeys() error {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	return sp.initSecurityKeysNoLock()
}

func (sp *UsmSecurityParameters) initSecurityKeysNoLock() error {
	var err error

	if sp.AuthenticationProtocol > NoAuth && len(sp.SecretKey) == 0 {
		sp.SecretKey, err = genlocalkey(sp.AuthenticationProtocol,
			sp.AuthenticationPassphrase,
			sp.AuthoritativeEngineID)
		if err != nil {
			return err
		}
	}
	if sp.PrivacyProtocol > NoPriv && len(sp.PrivacyKey) == 0 {
		switch sp.PrivacyProtocol {
		// Changed: The Output of SHA1 is a 20 octets array, therefore for AES128 (16 octets) either key extension algorithm can be used.
		case AES, AES192, AES256, AES192C, AES256C:
			// Use abstract AES key localization algorithms.
			sp.PrivacyKey, err = genlocalPrivKey(sp.PrivacyProtocol, sp.AuthenticationProtocol,
				sp.PrivacyPassphrase,
				sp.AuthoritativeEngineID)
			if err != nil {
				return err
			}
		default:
			sp.PrivacyKey, err = genlocalkey(sp.AuthenticationProtocol,
				sp.PrivacyPassphrase,
				sp.AuthoritativeEngineID)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (sp *UsmSecurityParameters) setSecurityParameters(in SnmpV3SecurityParameters) error {
	var insp *UsmSecurityParameters
	var err error

	sp.mu.Lock()
	defer sp.mu.Unlock()

	if insp, err = castUsmSecParams(in); err != nil {
		return err
	}

	if sp.AuthoritativeEngineID != insp.AuthoritativeEngineID {
		sp.AuthoritativeEngineID = insp.AuthoritativeEngineID
		sp.SecretKey = nil
		sp.PrivacyKey = nil

		err = sp.initSecurityKeysNoLock()
		if err != nil {
			return err
		}
	}
	sp.AuthoritativeEngineBoots = insp.AuthoritativeEngineBoots
	sp.AuthoritativeEngineTime = insp.AuthoritativeEngineTime

	return nil
}

func (sp *UsmSecurityParameters) validate(flags SnmpV3MsgFlags) error {
	securityLevel := flags & AuthPriv // isolate flags that determine security level

	switch securityLevel {
	case AuthPriv:
		if sp.PrivacyProtocol <= NoPriv {
			return fmt.Errorf("securityParameters.PrivacyProtocol is required")
		}
		fallthrough
	case AuthNoPriv:
		if sp.AuthenticationProtocol <= NoAuth {
			return fmt.Errorf("securityParameters.AuthenticationProtocol is required")
		}
		fallthrough
	case NoAuthNoPriv:
		if sp.UserName == "" {
			return fmt.Errorf("securityParameters.UserName is required")
		}
	default:
		return fmt.Errorf("validate: MsgFlags must be populated with an appropriate security level")
	}

	if sp.PrivacyProtocol > NoPriv && len(sp.PrivacyKey) == 0 {
		if sp.PrivacyPassphrase == "" {
			return fmt.Errorf("securityParameters.PrivacyPassphrase is required when a privacy protocol is specified")
		}
	}

	if sp.AuthenticationProtocol > NoAuth && len(sp.SecretKey) == 0 {
		if sp.AuthenticationPassphrase == "" {
			return fmt.Errorf("securityParameters.AuthenticationPassphrase is required when an authentication protocol is specified")
		}
	}

	return nil
}

func (sp *UsmSecurityParameters) init(log Logger) error {
	var err error

	sp.Logger = log

	switch sp.PrivacyProtocol {
	case AES, AES192, AES256, AES192C, AES256C:
		salt := make([]byte, 8)
		_, err = crand.Read(salt)
		if err != nil {
			return fmt.Errorf("error creating a cryptographically secure salt: %w", err)
		}
		sp.localAESSalt = binary.BigEndian.Uint64(salt)
	case DES:
		salt := make([]byte, 4)
		_, err = crand.Read(salt)
		if err != nil {
			return fmt.Errorf("error creating a cryptographically secure salt: %w", err)
		}
		sp.localDESSalt = binary.BigEndian.Uint32(salt)
	}

	return nil
}

func castUsmSecParams(secParams SnmpV3SecurityParameters) (*UsmSecurityParameters, error) {
	s, ok := secParams.(*UsmSecurityParameters)
	if !ok || s == nil {
		return nil, fmt.Errorf("param SnmpV3SecurityParameters is not of type *UsmSecurityParameters")
	}
	return s, nil
}

var (
	passwordKeyHashCache = make(map[string][]byte) //nolint:gochecknoglobals
	passwordKeyHashMutex sync.RWMutex              //nolint:gochecknoglobals
	passwordCacheDisable atomic.Bool               //nolint:gochecknoglobals
)

// PasswordCaching is enabled by default for performance reason. If the cache was disabled then
// re-enabled, the cache is reset.
func PasswordCaching(enable bool) {
	oldCacheEnable := !passwordCacheDisable.Load()
	passwordKeyHashMutex.Lock()
	if !enable { // if off
		passwordKeyHashCache = nil
	} else if !oldCacheEnable && enable { // if off then on
		passwordKeyHashCache = make(map[string][]byte)
	}
	passwordCacheDisable.Store(!enable)
	passwordKeyHashMutex.Unlock()
}

func hashPassword(hash hash.Hash, password string) ([]byte, error) {
	if len(password) == 0 {
		return []byte{}, errors.New("hashPassword: password is empty")
	}
	var pi int // password index
	for i := 0; i < 1048576; i += 64 {
		var chunk []byte
		for e := 0; e < 64; e++ {
			chunk = append(chunk, password[pi%len(password)])
			pi++
		}
		if _, err := hash.Write(chunk); err != nil {
			return []byte{}, err
		}
	}
	hashed := hash.Sum(nil)
	return hashed, nil
}

// Common passwordToKey algorithm, "caches" the result to avoid extra computation each reuse
func cachedPasswordToKey(hash hash.Hash, cacheKey string, password string) ([]byte, error) {
	cacheDisable := passwordCacheDisable.Load()
	if !cacheDisable {
		passwordKeyHashMutex.RLock()
		value := passwordKeyHashCache[cacheKey]
		passwordKeyHashMutex.RUnlock()

		if value != nil {
			return value, nil
		}
	}

	hashed, err := hashPassword(hash, password)
	if err != nil {
		return nil, err
	}

	if !cacheDisable {
		passwordKeyHashMutex.Lock()
		passwordKeyHashCache[cacheKey] = hashed
		passwordKeyHashMutex.Unlock()
	}

	return hashed, nil
}

func hMAC(hash crypto.Hash, cacheKey string, password string, engineID string) ([]byte, error) {
	hashed, err := cachedPasswordToKey(hash.New(), cacheKey, password)
	if err != nil {
		return []byte{}, nil
	}

	local := hash.New()
	_, err = local.Write(hashed)
	if err != nil {
		return []byte{}, err
	}

	_, err = local.Write([]byte(engineID))
	if err != nil {
		return []byte{}, err
	}

	_, err = local.Write(hashed)
	if err != nil {
		return []byte{}, err
	}

	final := local.Sum(nil)
	return final, nil
}

func cacheKey(authProtocol SnmpV3AuthProtocol, passphrase string) string {
	if passwordCacheDisable.Load() {
		return ""
	}
	var cacheKey = make([]byte, 1+len(passphrase))
	cacheKey = append(cacheKey, 'h'+byte(authProtocol))
	cacheKey = append(cacheKey, []byte(passphrase)...)
	return string(cacheKey)
}

// Extending the localized privacy key according to Reeder Key extension algorithm:
// https://tools.ietf.org/html/draft-reeder-snmpv3-usm-3dese
// Many vendors, including Cisco, use the 3DES key extension algorithm to extend the privacy keys that are too short when using AES,AES192 and AES256.
// Previously implemented in net-snmp and pysnmp libraries.
// Tested for AES128 and AES256
func extendKeyReeder(authProtocol SnmpV3AuthProtocol, password string, engineID string) ([]byte, error) {
	var key []byte
	var err error

	key, err = hMAC(authProtocol.HashType(), cacheKey(authProtocol, password), password, engineID)

	if err != nil {
		return nil, err
	}

	newkey, err := hMAC(authProtocol.HashType(), cacheKey(authProtocol, string(key)), string(key), engineID)

	return append(key, newkey...), err
}

// Extending the localized privacy key according to Blumenthal key extension algorithm:
// https://tools.ietf.org/html/draft-blumenthal-aes-usm-04#page-7
// Not many vendors use this algorithm.
// Previously implemented in the net-snmp and pysnmp libraries.
// TODO: Not tested
func extendKeyBlumenthal(authProtocol SnmpV3AuthProtocol, password string, engineID string) ([]byte, error) {
	var key []byte
	var err error

	key, err = hMAC(authProtocol.HashType(), cacheKey(authProtocol, password), password, engineID)

	if err != nil {
		return nil, err
	}

	newkey := authProtocol.HashType().New()
	_, _ = newkey.Write(key)
	return append(key, newkey.Sum(nil)...), err
}

// Changed: New function to calculate the Privacy Key for abstract AES
func genlocalPrivKey(privProtocol SnmpV3PrivProtocol, authProtocol SnmpV3AuthProtocol, password string, engineID string) ([]byte, error) {
	var keylen int
	var localPrivKey []byte
	var err error

	switch privProtocol {
	case AES, DES:
		keylen = 16
	case AES192, AES192C:
		keylen = 24
	case AES256, AES256C:
		keylen = 32
	}

	switch privProtocol {
	case AES, AES192C, AES256C:
		localPrivKey, err = extendKeyReeder(authProtocol, password, engineID)

	case AES192, AES256:
		localPrivKey, err = extendKeyBlumenthal(authProtocol, password, engineID)

	default:
		localPrivKey, err = genlocalkey(authProtocol, password, engineID)
	}

	if err != nil {
		return nil, err
	}

	if len(localPrivKey) < keylen {
		return []byte{}, fmt.Errorf("genlocalPrivKey: privProtocol: %v len(localPrivKey): %d, keylen: %d",
			privProtocol, len(localPrivKey), keylen)
	}

	return localPrivKey[:keylen], nil
}

func genlocalkey(authProtocol SnmpV3AuthProtocol, passphrase string, engineID string) ([]byte, error) {
	var secretKey []byte
	var err error

	secretKey, err = hMAC(authProtocol.HashType(), cacheKey(authProtocol, passphrase), passphrase, engineID)

	if err != nil {
		return []byte{}, err
	}

	return secretKey, nil
}

// http://tools.ietf.org/html/rfc2574#section-8.1.1.1
// localDESSalt needs to be incremented on every packet.
func (sp *UsmSecurityParameters) usmAllocateNewSalt() interface{} {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	var newSalt interface{}

	switch sp.PrivacyProtocol {
	case AES, AES192, AES256, AES192C, AES256C:
		newSalt = atomic.AddUint64(&(sp.localAESSalt), 1)
	default:
		newSalt = atomic.AddUint32(&(sp.localDESSalt), 1)
	}
	return newSalt
}

func (sp *UsmSecurityParameters) usmSetSalt(newSalt interface{}) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	switch sp.PrivacyProtocol {
	case AES, AES192, AES256, AES192C, AES256C:
		aesSalt, ok := newSalt.(uint64)
		if !ok {
			return fmt.Errorf("salt provided to usmSetSalt is not the correct type for the AES privacy protocol")
		}
		var salt = make([]byte, 8)
		binary.BigEndian.PutUint64(salt, aesSalt)
		sp.PrivacyParameters = salt
	default:
		desSalt, ok := newSalt.(uint32)
		if !ok {
			return fmt.Errorf("salt provided to usmSetSalt is not the correct type for the DES privacy protocol")
		}
		var salt = make([]byte, 8)
		binary.BigEndian.PutUint32(salt, sp.AuthoritativeEngineBoots)
		binary.BigEndian.PutUint32(salt[4:], desSalt)
		sp.PrivacyParameters = salt
	}
	return nil
}

// InitPacket ensures the enc salt is incremented for packets marked for AuthPriv
func (sp *UsmSecurityParameters) InitPacket(packet *SnmpPacket) error {
	// http://tools.ietf.org/html/rfc2574#section-8.1.1.1
	// localDESSalt needs to be incremented on every packet.
	newSalt := sp.usmAllocateNewSalt()
	if packet.MsgFlags&AuthPriv > AuthNoPriv {
		s, err := castUsmSecParams(packet.SecurityParameters)
		if err != nil {
			return err
		}
		return s.usmSetSalt(newSalt)
	}
	return nil
}

func (sp *UsmSecurityParameters) discoveryRequired() *SnmpPacket {
	if sp.AuthoritativeEngineID == "" {
		var emptyPdus []SnmpPDU

		// send blank packet to discover authoriative engine ID/boots/time
		blankPacket := &SnmpPacket{
			Version:            Version3,
			MsgFlags:           Reportable | NoAuthNoPriv,
			SecurityModel:      UserSecurityModel,
			SecurityParameters: &UsmSecurityParameters{Logger: sp.Logger},
			PDUType:            GetRequest,
			Logger:             sp.Logger,
			Variables:          emptyPdus,
		}

		return blankPacket
	}
	return nil
}

func (sp *UsmSecurityParameters) calcPacketDigest(packet []byte) ([]byte, error) {
	return calcPacketDigest(packet, sp)
}

// calcPacketDigest calculate authenticate digest for incoming messages (TRAP or
// INFORM).
// Support MD5, SHA1, SHA224, SHA256, SHA384, SHA512 protocols
func calcPacketDigest(packetBytes []byte, secParams *UsmSecurityParameters) ([]byte, error) {
	var digest []byte
	var err error

	switch secParams.AuthenticationProtocol {
	case MD5, SHA:
		digest, err = digestRFC3414(
			secParams.AuthenticationProtocol,
			packetBytes,
			secParams.SecretKey)
	case SHA224, SHA256, SHA384, SHA512:
		digest, err = digestRFC7860(
			secParams.AuthenticationProtocol,
			packetBytes,
			secParams.SecretKey)
	}
	if err != nil {
		return nil, err
	}

	digest = digest[:len(macVarbinds[secParams.AuthenticationProtocol])-2]

	return digest, nil
}

// digestRFC7860 calculate digest for incoming messages using HMAC-SHA2 protcols
// according to RFC7860 4.2.2
func digestRFC7860(h SnmpV3AuthProtocol, packet []byte, authKey []byte) ([]byte, error) {
	mac := hmac.New(h.HashType().New, authKey)
	_, err := mac.Write(packet)
	if err != nil {
		return []byte{}, err
	}
	msgDigest := mac.Sum(nil)
	return msgDigest, nil
}

// digestRFC3414 calculate digest for incoming messages using MD5 or SHA1
// according to RFC3414 6.3.2 and 7.3.2
func digestRFC3414(h SnmpV3AuthProtocol, packet []byte, authKey []byte) ([]byte, error) {
	var extkey [64]byte
	var err error
	var k1, k2 [64]byte
	var h1, h2 hash.Hash

	copy(extkey[:], authKey)

	switch h {
	case MD5:
		h1 = md5.New() //nolint:gosec
		h2 = md5.New() //nolint:gosec
	case SHA:
		h1 = sha1.New() //nolint:gosec
		h2 = sha1.New() //nolint:gosec
	}

	for i := 0; i < 64; i++ {
		k1[i] = extkey[i] ^ 0x36
		k2[i] = extkey[i] ^ 0x5c
	}

	_, err = h1.Write(k1[:])
	if err != nil {
		return []byte{}, err
	}

	_, err = h1.Write(packet)
	if err != nil {
		return []byte{}, err
	}

	d1 := h1.Sum(nil)

	_, err = h2.Write(k2[:])
	if err != nil {
		return []byte{}, err
	}

	_, err = h2.Write(d1)
	if err != nil {
		return []byte{}, err
	}

	return h2.Sum(nil)[:12], nil
}

func (sp *UsmSecurityParameters) authenticate(packet []byte) error {
	var msgDigest []byte
	var err error

	if msgDigest, err = sp.calcPacketDigest(packet); err != nil {
		return err
	}

	idx := bytes.Index(packet, macVarbinds[sp.AuthenticationProtocol])

	if idx < 0 {
		return fmt.Errorf("unable to locate the position in packet to write authentication key")
	}

	copy(packet[idx+2:idx+len(macVarbinds[sp.AuthenticationProtocol])], msgDigest)
	return nil
}

// determine whether a message is authentic
func (sp *UsmSecurityParameters) isAuthentic(packetBytes []byte, packet *SnmpPacket) (bool, error) {
	var msgDigest []byte
	var packetSecParams *UsmSecurityParameters
	var err error

	if packetSecParams, err = castUsmSecParams(packet.SecurityParameters); err != nil {
		return false, err
	}

	// Verify the username
	if packetSecParams.UserName != sp.UserName {
		return false, nil
	}

	// TODO: investigate call chain to determine if this is really the best spot for this
	if msgDigest, err = calcPacketDigest(packetBytes, packetSecParams); err != nil {
		return false, err
	}

	// Check the message signature against the computed digest
	signature := []byte(packetSecParams.AuthenticationParameters)
	return subtle.ConstantTimeCompare(msgDigest, signature) == 1, nil
}

func (sp *UsmSecurityParameters) encryptPacket(scopedPdu []byte) ([]byte, error) {
	var b []byte

	switch sp.PrivacyProtocol {
	case AES, AES192, AES256, AES192C, AES256C:
		var iv [16]byte
		binary.BigEndian.PutUint32(iv[:], sp.AuthoritativeEngineBoots)
		binary.BigEndian.PutUint32(iv[4:], sp.AuthoritativeEngineTime)
		copy(iv[8:], sp.PrivacyParameters)
		// aes.NewCipher(sp.PrivacyKey[:16]) changed to aes.NewCipher(sp.PrivacyKey)
		block, err := aes.NewCipher(sp.PrivacyKey)
		if err != nil {
			return nil, err
		}
		stream := cipher.NewCFBEncrypter(block, iv[:])
		ciphertext := make([]byte, len(scopedPdu))
		stream.XORKeyStream(ciphertext, scopedPdu)
		pduLen, err := marshalLength(len(ciphertext))
		if err != nil {
			return nil, err
		}
		b = append([]byte{byte(OctetString)}, pduLen...)
		scopedPdu = append(b, ciphertext...) //nolint:gocritic
	case DES:
		preiv := sp.PrivacyKey[8:]
		var iv [8]byte
		for i := 0; i < len(iv); i++ {
			iv[i] = preiv[i] ^ sp.PrivacyParameters[i]
		}
		block, err := des.NewCipher(sp.PrivacyKey[:8]) //nolint:gosec
		if err != nil {
			return nil, err
		}
		mode := cipher.NewCBCEncrypter(block, iv[:])

		pad := make([]byte, des.BlockSize-len(scopedPdu)%des.BlockSize)
		scopedPdu = append(scopedPdu, pad...)

		ciphertext := make([]byte, len(scopedPdu))
		mode.CryptBlocks(ciphertext, scopedPdu)
		pduLen, err := marshalLength(len(ciphertext))
		if err != nil {
			return nil, err
		}
		b = append([]byte{byte(OctetString)}, pduLen...)
		scopedPdu = append(b, ciphertext...) //nolint:gocritic
	}

	return scopedPdu, nil
}

func (sp *UsmSecurityParameters) decryptPacket(packet []byte, cursor int) ([]byte, error) {
	_, cursorTmp, err := parseLength(packet[cursor:])
	if err != nil {
		return nil, err
	}
	cursorTmp += cursor
	if cursorTmp > len(packet) {
		return nil, errors.New("error decrypting ScopedPDU: truncated packet")
	}

	switch sp.PrivacyProtocol {
	case AES, AES192, AES256, AES192C, AES256C:
		var iv [16]byte
		binary.BigEndian.PutUint32(iv[:], sp.AuthoritativeEngineBoots)
		binary.BigEndian.PutUint32(iv[4:], sp.AuthoritativeEngineTime)
		copy(iv[8:], sp.PrivacyParameters)

		block, err := aes.NewCipher(sp.PrivacyKey)
		if err != nil {
			return nil, err
		}
		stream := cipher.NewCFBDecrypter(block, iv[:])
		plaintext := make([]byte, len(packet[cursorTmp:]))
		stream.XORKeyStream(plaintext, packet[cursorTmp:])
		copy(packet[cursor:], plaintext)
		packet = packet[:cursor+len(plaintext)]
	case DES:
		if len(packet[cursorTmp:])%des.BlockSize != 0 {
			return nil, errors.New("error decrypting ScopedPDU: not multiple of des block size")
		}
		preiv := sp.PrivacyKey[8:]
		var iv [8]byte
		for i := 0; i < len(iv); i++ {
			iv[i] = preiv[i] ^ sp.PrivacyParameters[i]
		}
		block, err := des.NewCipher(sp.PrivacyKey[:8]) //nolint:gosec
		if err != nil {
			return nil, err
		}
		mode := cipher.NewCBCDecrypter(block, iv[:])

		plaintext := make([]byte, len(packet[cursorTmp:]))
		mode.CryptBlocks(plaintext, packet[cursorTmp:])
		copy(packet[cursor:], plaintext)
		// truncate packet to remove extra space caused by the
		// octetstring/length header that was just replaced
		packet = packet[:cursor+len(plaintext)]
	}
	return packet, nil
}

// marshal a snmp version 3 security parameters field for the User Security Model
func (sp *UsmSecurityParameters) marshal(flags SnmpV3MsgFlags) ([]byte, error) {
	var buf bytes.Buffer
	var err error

	// msgAuthoritativeEngineID
	buf.Write([]byte{byte(OctetString), byte(len(sp.AuthoritativeEngineID))})
	buf.WriteString(sp.AuthoritativeEngineID)

	// msgAuthoritativeEngineBoots
	msgAuthoritativeEngineBoots, err := marshalUint32(sp.AuthoritativeEngineBoots)
	if err != nil {
		return nil, err
	}
	buf.Write([]byte{byte(Integer), byte(len(msgAuthoritativeEngineBoots))})
	buf.Write(msgAuthoritativeEngineBoots)

	// msgAuthoritativeEngineTime
	msgAuthoritativeEngineTime, err := marshalUint32(sp.AuthoritativeEngineTime)
	if err != nil {
		return nil, err
	}
	buf.Write([]byte{byte(Integer), byte(len(msgAuthoritativeEngineTime))})
	buf.Write(msgAuthoritativeEngineTime)

	// msgUserName
	buf.Write([]byte{byte(OctetString), byte(len(sp.UserName))})
	buf.WriteString(sp.UserName)

	// msgAuthenticationParameters
	if flags&AuthNoPriv > 0 {
		buf.Write(macVarbinds[sp.AuthenticationProtocol])
	} else {
		buf.Write([]byte{byte(OctetString), 0})
	}
	// msgPrivacyParameters
	if flags&AuthPriv > AuthNoPriv {
		privlen, err2 := marshalLength(len(sp.PrivacyParameters))
		if err2 != nil {
			return nil, err2
		}
		buf.Write([]byte{byte(OctetString)})
		buf.Write(privlen)
		buf.Write(sp.PrivacyParameters)
	} else {
		buf.Write([]byte{byte(OctetString), 0})
	}

	// wrap security parameters in a sequence
	paramLen, err := marshalLength(buf.Len())
	if err != nil {
		return nil, err
	}
	tmpseq := append([]byte{byte(Sequence)}, paramLen...)
	tmpseq = append(tmpseq, buf.Bytes()...)

	return tmpseq, nil
}

func (sp *UsmSecurityParameters) unmarshal(flags SnmpV3MsgFlags, packet []byte, cursor int) (int, error) {
	var err error

	if cursor >= len(packet) {
		return 0, errors.New("error parsing SNMPV3 User Security Model parameters: end of packet")
	}

	if PDUType(packet[cursor]) != Sequence {
		return 0, errors.New("error parsing SNMPV3 User Security Model parameters")
	}
	_, cursorTmp, err := parseLength(packet[cursor:])
	if err != nil {
		return 0, err
	}
	cursor += cursorTmp
	if cursorTmp > len(packet) {
		return 0, errors.New("error parsing SNMPV3 User Security Model parameters: truncated packet")
	}

	rawMsgAuthoritativeEngineID, count, err := parseRawField(sp.Logger, packet[cursor:], "msgAuthoritativeEngineID")
	if err != nil {
		return 0, fmt.Errorf("error parsing SNMPV3 User Security Model msgAuthoritativeEngineID: %w", err)
	}
	cursor += count
	if AuthoritativeEngineID, ok := rawMsgAuthoritativeEngineID.(string); ok {
		if sp.AuthoritativeEngineID != AuthoritativeEngineID {
			sp.AuthoritativeEngineID = AuthoritativeEngineID
			sp.SecretKey = nil
			sp.PrivacyKey = nil

			sp.Logger.Printf("Parsed authoritativeEngineID %0x", []byte(AuthoritativeEngineID))
			err = sp.initSecurityKeysNoLock()
			if err != nil {
				return 0, err
			}
		}
	}

	rawMsgAuthoritativeEngineBoots, count, err := parseRawField(sp.Logger, packet[cursor:], "msgAuthoritativeEngineBoots")
	if err != nil {
		return 0, fmt.Errorf("error parsing SNMPV3 User Security Model msgAuthoritativeEngineBoots: %w", err)
	}
	cursor += count
	if AuthoritativeEngineBoots, ok := rawMsgAuthoritativeEngineBoots.(int); ok {
		sp.AuthoritativeEngineBoots = uint32(AuthoritativeEngineBoots) //nolint:gosec
		sp.Logger.Printf("Parsed authoritativeEngineBoots %d", AuthoritativeEngineBoots)
	}

	rawMsgAuthoritativeEngineTime, count, err := parseRawField(sp.Logger, packet[cursor:], "msgAuthoritativeEngineTime")
	if err != nil {
		return 0, fmt.Errorf("error parsing SNMPV3 User Security Model msgAuthoritativeEngineTime: %w", err)
	}
	cursor += count
	if AuthoritativeEngineTime, ok := rawMsgAuthoritativeEngineTime.(int); ok {
		sp.AuthoritativeEngineTime = uint32(AuthoritativeEngineTime) //nolint:gosec
		sp.Logger.Printf("Parsed authoritativeEngineTime %d", AuthoritativeEngineTime)
	}

	rawMsgUserName, count, err := parseRawField(sp.Logger, packet[cursor:], "msgUserName")
	if err != nil {
		return 0, fmt.Errorf("error parsing SNMPV3 User Security Model msgUserName: %w", err)
	}
	cursor += count
	if msgUserName, ok := rawMsgUserName.(string); ok {
		sp.UserName = msgUserName
		sp.Logger.Printf("Parsed userName %s", msgUserName)
	}

	rawMsgAuthParameters, count, err := parseRawField(sp.Logger, packet[cursor:], "msgAuthenticationParameters")
	if err != nil {
		return 0, fmt.Errorf("error parsing SNMPV3 User Security Model msgAuthenticationParameters: %w", err)
	}
	if msgAuthenticationParameters, ok := rawMsgAuthParameters.(string); ok {
		sp.AuthenticationParameters = msgAuthenticationParameters
		sp.Logger.Printf("Parsed authenticationParameters %s", msgAuthenticationParameters)
	}
	// blank msgAuthenticationParameters to prepare for authentication check later
	if flags&AuthNoPriv > 0 {
		// In case if the authentication protocol is not configured or set to NoAuth, then the packet cannot
		// be processed further
		if sp.AuthenticationProtocol <= NoAuth {
			return 0, errors.New("error parsing SNMPv3 User Security Model: authentication parameters are not configured to parse incoming authenticated message")
		}
		copy(packet[cursor+2:cursor+len(macVarbinds[sp.AuthenticationProtocol])], macVarbinds[sp.AuthenticationProtocol][2:])
	}
	cursor += count

	rawMsgPrivacyParameters, count, err := parseRawField(sp.Logger, packet[cursor:], "msgPrivacyParameters")
	if err != nil {
		return 0, fmt.Errorf("error parsing SNMPV3 User Security Model msgPrivacyParameters: %w", err)
	}
	cursor += count
	if msgPrivacyParameters, ok := rawMsgPrivacyParameters.(string); ok {
		sp.PrivacyParameters = []byte(msgPrivacyParameters)
		sp.Logger.Printf("Parsed privacyParameters %s", msgPrivacyParameters)
		if flags&AuthPriv >= AuthPriv {
			if sp.PrivacyProtocol <= NoPriv {
				return 0, errors.New("error parsing SNMPv3 User Security Model: privacy parameters are not configured to parse incoming encrypted message")
			}
		}
	}

	return cursor, nil
}
