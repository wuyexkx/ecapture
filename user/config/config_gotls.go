// Copyright 2022 CFC4N <cfc4n.cs@gmail.com>. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// Copyright © 2022 Hengqi Chen
package config

import (
	"debug/elf"
	"errors"
	"fmt"
	"os"
	"runtime"

	"golang.org/x/arch/arm64/arm64asm"
	"golang.org/x/arch/x86/x86asm"
)

const (
	// Arm64armInstSize via :  arm64/arm64asm/decode.go:Decode() size = 4
	Arm64armInstSize = 4

	GoTlsReadFunc = "crypto/tls.(*Conn).Read"
)

var (
	ErrorGoBINNotFound  = errors.New("The executable program (compiled by Golang) was not found")
	ErrorSymbolNotFound = errors.New("symbol not found")
	ErrorNoRetFound     = errors.New("no RET instructions found")
)

// GoTLSConfig represents configuration for Go SSL probe
type GoTLSConfig struct {
	eConfig
	Path         string    `json:"path"`       // golang application path to binary built with Go toolchain.
	PcapFile     string    `json:"pcapFile"`   // pcapFile  the  raw  packets  to file rather than parsing and printing them out.
	KeylogFile   string    `json:"keylogFile"` // keylogFile  The file stores SSL/TLS keys, and eCapture captures these keys during encrypted traffic communication and saves them to the file.
	Model        string    `json:"model"`      // model  such as : text, pcapng/pcap, key/keylog.
	Ifname       string    `json:"ifName"`     // (TC Classifier) Interface name on which the probe will be attached.
	PcapFilter   string    `json:"pcapFilter"` // pcap filter
	goElfArch    string    //
	goElf        *elf.File //
	ReadTlsAddrs []int
}

// NewGoTLSConfig creates a new config for Go SSL
func NewGoTLSConfig() *GoTLSConfig {
	gc := &GoTLSConfig{}
	gc.PerCpuMapSize = DefaultMapSizePerCpu
	return gc
}

func (gc *GoTLSConfig) Check() error {
	var err error
	if gc.Path == "" {
		return ErrorGoBINNotFound
	}

	_, err = gc.checkModel()
	if err != nil {
		return err
	}
	_, err = os.Stat(gc.Path)
	if err != nil {
		return err
	}

	var goElf *elf.File
	goElf, err = elf.Open(gc.Path)
	if err != nil {
		return err
	}

	var goElfArch string
	switch goElf.FileHeader.Machine.String() {
	case elf.EM_AARCH64.String():
		goElfArch = "arm64"
	case elf.EM_X86_64.String():
		goElfArch = "amd64"
	default:
		goElfArch = "unsupport_arch"
	}

	if goElfArch != runtime.GOARCH {
		err = fmt.Errorf("Go Application not match, want:%s, have:%s", runtime.GOARCH, goElfArch)
		return err
	}
	switch goElfArch {
	case "amd64":
	case "arm64":
	default:
		return fmt.Errorf("unsupport CPU arch :%s", goElfArch)
	}
	gc.goElfArch = goElfArch
	gc.goElf = goElf
	gc.ReadTlsAddrs, err = gc.findRetOffsets(GoTlsReadFunc)
	return err
}

// FindRetOffsets searches for the addresses of all RET instructions within
// the instruction set associated with the specified symbol in an ELF program.
// It is used for mounting uretprobe programs for Golang programs,
// which are actually mounted via uprobe on these addresses.
func (gc *GoTLSConfig) findRetOffsets(symbolName string) ([]int, error) {
	var err error
	var allSymbs []elf.Symbol

	goSymbs, _ := gc.goElf.Symbols()
	if len(goSymbs) > 0 {
		allSymbs = append(allSymbs, goSymbs...)
	}

	goDynamicSymbs, _ := gc.goElf.DynamicSymbols()
	if len(goDynamicSymbs) > 0 {
		allSymbs = append(allSymbs, goDynamicSymbs...)
	}

	if len(allSymbs) == 0 {
		return nil, fmt.Errorf("no symbols found")
	}

	var found bool
	var symbol elf.Symbol
	for _, s := range allSymbs {
		if s.Name == symbolName {
			symbol = s
			found = true
			break
		}
	}

	if !found {
		return nil, ErrorSymbolNotFound
	}

	section := gc.goElf.Sections[symbol.Section]

	var elfText []byte
	elfText, err = section.Data()
	if err != nil {
		return nil, err
	}

	start := symbol.Value - section.Addr
	end := start + symbol.Size

	var offsets []int
	var instHex []byte
	instHex = elfText[start:end]
	offsets, _ = gc.decodeInstruction(instHex)
	if len(offsets) == 0 {
		return offsets, ErrorNoRetFound
	}
	return offsets, nil
}

// decodeInstruction Decode into assembly instructions and identify the RET instruction to return the offset.
func (gc *GoTLSConfig) decodeInstruction(instHex []byte) ([]int, error) {
	var offsets []int
	for i := 0; i < len(instHex); {
		if gc.goElfArch == "amd64" {
			inst, err := x86asm.Decode(instHex[i:], 64)
			if err != nil {
				return nil, err
			}
			if inst.Op == x86asm.RET {
				offsets = append(offsets, i)
			}
			i += inst.Len
		} else {
			inst, err := arm64asm.Decode(instHex[i:])
			if err != nil {
				return nil, err
			}
			if inst.Op == arm64asm.RET {
				offsets = append(offsets, i)
			}
			i += Arm64armInstSize
		}
	}
	return offsets, nil
}

func (gc *GoTLSConfig) checkModel() (string, error) {
	var m string
	var e error
	switch gc.Model {
	case TlsCaptureModelKeylog, TlsCaptureModelKey:
		m = TlsCaptureModelKey
	case TlsCaptureModelPcap, TlsCaptureModelPcapng:
		m = TlsCaptureModelPcap
		if gc.Ifname == "" {
			return "", errors.New("'pcap' model used, please used -i flag to set ifname value.")
		}
		fmt.Println(gc.Ifname)
	default:
		m = TlsCaptureModelText
	}
	return m, e
}
