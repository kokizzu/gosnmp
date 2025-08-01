// Copyright 2012 The GoSNMP Authors. All rights reserved.  Use of this
// source code is governed by a BSD-style license that can be found in the
// LICENSE file.

package gosnmp

import (
	"fmt"
	"strings"
)

func (x *GoSNMP) walk(getRequestType PDUType, rootOid string, walkFn WalkFunc) error {
	// If no rootOid is provided, fall back to the 'internet' subtree (.1.3.6.1).
	// This ensures visibility of both standard (e.g. MIB-2) and vendor-specific branches.
	// It also guarantees the OID is valid for BER encoding:
	// - RFC 2578 §7.1.3: OIDs must have at least two sub-identifiers
	// - X.690 §8.19: the first two arcs are encoded as (40 * arc1 + arc2)
	if rootOid == "" || rootOid == "." {
		// IANA 'internet' subtree under ISO OID structure per X.660.
		// See https://oidref.com/1.3.6.1
		rootOid = ".1.3.6.1"
	}

	if !strings.HasPrefix(rootOid, ".") {
		rootOid = string(".") + rootOid
	}

	oid := rootOid
	requests := 0
	maxReps := x.MaxRepetitions
	if maxReps == 0 {
		maxReps = defaultMaxRepetitions
	}

	// AppOpt 'c: do not check returned OIDs are increasing'
	checkIncreasing := true
	if x.AppOpts != nil {
		if _, ok := x.AppOpts["c"]; ok {
			if getRequestType == GetBulkRequest || getRequestType == GetNextRequest {
				checkIncreasing = false
			}
		}
	}

RequestLoop:
	for {
		requests++

		var response *SnmpPacket
		var err error

		switch getRequestType {
		case GetBulkRequest:
			response, err = x.GetBulk([]string{oid}, 0, maxReps)
		case GetNextRequest:
			response, err = x.GetNext([]string{oid})
		case GetRequest:
			response, err = x.Get([]string{oid})
		default:
			response, err = nil, fmt.Errorf("unsupported request type: %d", getRequestType)
		}

		if err != nil {
			return err
		}
		if len(response.Variables) == 0 {
			break RequestLoop
		}

		switch response.Error {
		case TooBig:
			x.Logger.Print("Walk terminated with TooBig")
			break RequestLoop
		case NoSuchName:
			x.Logger.Print("Walk terminated with NoSuchName")
			break RequestLoop
		case BadValue:
			x.Logger.Print("Walk terminated with BadValue")
			break RequestLoop
		case ReadOnly:
			x.Logger.Print("Walk terminated with ReadOnly")
			break RequestLoop
		case GenErr:
			x.Logger.Print("Walk terminated with GenErr")
			break RequestLoop
		case NoAccess:
			x.Logger.Print("Walk terminated with NoAccess")
			break RequestLoop
		case WrongType:
			x.Logger.Print("Walk terminated with WrongType")
			break RequestLoop
		case WrongLength:
			x.Logger.Print("Walk terminated with WrongLength")
			break RequestLoop
		case WrongEncoding:
			x.Logger.Print("Walk terminated with WrongEncoding")
			break RequestLoop
		case WrongValue:
			x.Logger.Print("Walk terminated with WrongValue")
			break RequestLoop
		case NoCreation:
			x.Logger.Print("Walk terminated with NoCreation")
			break RequestLoop
		case InconsistentValue:
			x.Logger.Print("Walk terminated with InconsistentValue")
			break RequestLoop
		case ResourceUnavailable:
			x.Logger.Print("Walk terminated with ResourceUnavailable")
			break RequestLoop
		case CommitFailed:
			x.Logger.Print("Walk terminated with CommitFailed")
			break RequestLoop
		case UndoFailed:
			x.Logger.Print("Walk terminated with UndoFailed")
			break RequestLoop
		case AuthorizationError:
			x.Logger.Print("Walk terminated with AuthorizationError")
			break RequestLoop
		case NotWritable:
			x.Logger.Print("Walk terminated with NotWritable")
			break RequestLoop
		case InconsistentName:
			x.Logger.Print("Walk terminated with InconsistentName")
			break RequestLoop
		case NoError:
			x.Logger.Print("Walk completed with NoError")
		}

		for i, pdu := range response.Variables {
			if pdu.Type == EndOfMibView || pdu.Type == NoSuchObject || pdu.Type == NoSuchInstance {
				x.Logger.Printf("BulkWalk terminated with type 0x%x", pdu.Type)
				break RequestLoop
			}
			if !strings.HasPrefix(pdu.Name, rootOid+".") {
				// Not in the requested root range.
				// if this is the first request, and the first variable in that request
				// and this condition is triggered - the first result is out of range
				// need to perform a regular get request
				// this request has been too narrowly defined to be found with a getNext
				// Issue #78 #93
				if requests == 1 && i == 0 {
					getRequestType = GetRequest
					continue RequestLoop
				} else if pdu.Name == rootOid && pdu.Type != NoSuchInstance {
					// Call walk function if the pdu instance is found
					// considering that the rootOid is a leafOid
					if err := walkFn(pdu); err != nil {
						return err
					}
				}
				break RequestLoop
			}

			if checkIncreasing && pdu.Name == oid {
				return fmt.Errorf("OID not increasing: %s", pdu.Name)
			}

			// Report our pdu
			if err := walkFn(pdu); err != nil {
				return err
			}
		}
		// Save last oid for next request
		oid = response.Variables[len(response.Variables)-1].Name
	}
	x.Logger.Printf("BulkWalk completed in %d requests", requests)
	return nil
}

func (x *GoSNMP) walkAll(getRequestType PDUType, rootOid string) (results []SnmpPDU, err error) {
	err = x.walk(getRequestType, rootOid, func(dataUnit SnmpPDU) error {
		results = append(results, dataUnit)
		return nil
	})
	return results, err
}
