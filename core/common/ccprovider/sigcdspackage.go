/*
Copyright IBM Corp. 2017 All Rights Reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

		 http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package ccprovider

import (
	"fmt"
	"io/ioutil"
	"os"

	"github.com/golang/protobuf/proto"

	"bytes"

	"github.com/hyperledger/fabric/bccsp"
	"github.com/hyperledger/fabric/bccsp/factory"
	"github.com/hyperledger/fabric/core/common/ccpackage"
	"github.com/hyperledger/fabric/protos/common"
	pb "github.com/hyperledger/fabric/protos/peer"
)

//----- SignedCDSData ------

//SignedCDSData is data stored in the LCCC on instantiation of a CC
//for SignedCDSPackage. This needs to be serialized for ChaincodeData
//hence the protobuf format
type SignedCDSData struct {
	CodeHash      []byte `protobuf:"bytes,1,opt,name=hash"`
	MetaDataHash  []byte `protobuf:"bytes,2,opt,name=metadatahash"`
	SignatureHash []byte `protobuf:"bytes,3,opt,name=signaturehash"`
}

//----implement functions needed from proto.Message for proto's mar/unmarshal functions

//Reset resets
func (data *SignedCDSData) Reset() { *data = SignedCDSData{} }

//String convers to string
func (data *SignedCDSData) String() string { return proto.CompactTextString(data) }

//ProtoMessage just exists to make proto happy
func (*SignedCDSData) ProtoMessage() {}

//Equals data equals other
func (data *SignedCDSData) Equals(other *SignedCDSData) bool {
	return other != nil &&
		bytes.Equal(data.CodeHash, other.CodeHash) &&
		bytes.Equal(data.MetaDataHash, other.MetaDataHash) &&
		bytes.Equal(data.SignatureHash, other.SignatureHash)
}

//-------- SignedCDSPackage ---------

//SignedCDSPackage encapsulates SignedChaincodeDeploymentSpec.
type SignedCDSPackage struct {
	buf      []byte
	depSpec  *pb.ChaincodeDeploymentSpec
	sDepSpec *pb.SignedChaincodeDeploymentSpec
	env      *common.Envelope
	data     *SignedCDSData
}

// GetDepSpec gets the ChaincodeDeploymentSpec from the package
func (ccpack *SignedCDSPackage) GetDepSpec() *pb.ChaincodeDeploymentSpec {
	return ccpack.depSpec
}

// GetPackageObject gets the ChaincodeDeploymentSpec as proto.Message
func (ccpack *SignedCDSPackage) GetPackageObject() proto.Message {
	return ccpack.env
}

func (ccpack *SignedCDSPackage) getCDSData(scds *pb.SignedChaincodeDeploymentSpec) ([]byte, *SignedCDSData, error) {
	// check for nil argument. It is an assertion that getCDSData
	// is never called on a package that did not go through/succeed
	// package initialization.
	if scds == nil {
		panic("nil cds")
	}

	cds := &pb.ChaincodeDeploymentSpec{}
	err := proto.Unmarshal(scds.ChaincodeDeploymentSpec, cds)
	if err != nil {
		return nil, nil, err
	}

	if err = factory.InitFactories(nil); err != nil {
		return nil, nil, fmt.Errorf("Internal error, BCCSP could not be initialized : %s", err)
	}

	//get the hash object
	hash, err := factory.GetDefault().GetHash(&bccsp.SHAOpts{})
	if err != nil {
		return nil, nil, err
	}

	scdsdata := &SignedCDSData{}

	//get the code hash
	scdsdata.CodeHash = hash.Sum(cds.CodePackage)

	hash.Reset()

	//get the metadata hash
	hash.Write([]byte(cds.ChaincodeSpec.ChaincodeId.Name))
	hash.Write([]byte(cds.ChaincodeSpec.ChaincodeId.Version))

	scdsdata.MetaDataHash = hash.Sum(nil)

	hash.Reset()

	//get the signature hashes
	if scds.InstantiationPolicy == nil {
		return nil, nil, fmt.Errorf("instantiation policy cannot be nil for chaincode (%s:%s)", cds.ChaincodeSpec.ChaincodeId.Name, cds.ChaincodeSpec.ChaincodeId.Version)
	}

	hash.Write(scds.InstantiationPolicy)
	for _, o := range scds.OwnerEndorsements {
		hash.Write(o.Endorser)
	}
	scdsdata.SignatureHash = hash.Sum(nil)

	//marshall data
	b, err := proto.Marshal(scdsdata)
	if err != nil {
		return nil, nil, err
	}

	return b, scdsdata, nil
}

// ValidateCC returns error if the chaincode is not found or if its not a
// ChaincodeDeploymentSpec
func (ccpack *SignedCDSPackage) ValidateCC(ccdata *ChaincodeData) (*pb.ChaincodeDeploymentSpec, error) {
	if ccpack.sDepSpec == nil {
		return nil, fmt.Errorf("uninitialized package")
	}

	if ccpack.sDepSpec.ChaincodeDeploymentSpec == nil {
		return nil, fmt.Errorf("signed chaincode deployment spec cannot be nil in a package")
	}

	if ccpack.depSpec == nil {
		return nil, fmt.Errorf("chaincode deployment spec cannot be nil in a package")
	}

	if ccdata.Name != ccpack.depSpec.ChaincodeSpec.ChaincodeId.Name || ccdata.Version != ccpack.depSpec.ChaincodeSpec.ChaincodeId.Version {
		return nil, fmt.Errorf("invalid chaincode data %v (%v)", ccdata, ccpack.depSpec.ChaincodeSpec.ChaincodeId)
	}

	otherdata := &SignedCDSData{}
	err := proto.Unmarshal(ccdata.Data, otherdata)
	if err != nil {
		return nil, err
	}

	if !ccpack.data.Equals(otherdata) {
		return nil, fmt.Errorf("data mismatch")
	}

	return ccpack.depSpec, nil
}

//InitFromBuffer sets the buffer if valid and returns ChaincodeData
func (ccpack *SignedCDSPackage) InitFromBuffer(buf []byte) (*ChaincodeData, error) {
	//incase ccpack is reused
	ccpack.buf = nil
	ccpack.sDepSpec = nil
	ccpack.depSpec = nil
	ccpack.env = nil
	ccpack.data = nil

	env := &common.Envelope{}
	err := proto.Unmarshal(buf, env)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal envelope from bytes")
	}
	cHdr, sDepSpec, err := ccpackage.ExtractSignedCCDepSpec(env)
	if err != nil {
		return nil, err
	}
	if cHdr.Type != int32(common.HeaderType_CHAINCODE_PACKAGE) {
		return nil, fmt.Errorf("invalid type of envelope for chaincode package")
	}

	depSpec := &pb.ChaincodeDeploymentSpec{}
	err = proto.Unmarshal(sDepSpec.ChaincodeDeploymentSpec, depSpec)
	if err != nil {
		return nil, fmt.Errorf("error getting deployment spec")
	}

	databytes, data, err := ccpack.getCDSData(sDepSpec)
	if err != nil {
		return nil, err
	}

	ccpack.buf = buf
	ccpack.sDepSpec = sDepSpec
	ccpack.depSpec = depSpec
	ccpack.env = env
	ccpack.data = data

	return &ChaincodeData{Name: depSpec.ChaincodeSpec.ChaincodeId.Name, Version: depSpec.ChaincodeSpec.ChaincodeId.Version, Data: databytes}, nil
}

//InitFromFS returns the chaincode and its package from the file system
func (ccpack *SignedCDSPackage) InitFromFS(ccname string, ccversion string) ([]byte, *pb.ChaincodeDeploymentSpec, error) {
	//incase ccpack is reused
	ccpack.buf = nil
	ccpack.sDepSpec = nil
	ccpack.depSpec = nil
	ccpack.env = nil

	buf, err := GetChaincodePackage(ccname, ccversion)
	if err != nil {
		return nil, nil, err
	}

	if _, err = ccpack.InitFromBuffer(buf); err != nil {
		return nil, nil, err
	}

	return ccpack.buf, ccpack.depSpec, nil
}

//PutChaincodeToFS - serializes chaincode to a package on the file system
func (ccpack *SignedCDSPackage) PutChaincodeToFS() error {
	if ccpack.buf == nil {
		return fmt.Errorf("uninitialized package")
	}

	if ccpack.sDepSpec == nil || ccpack.depSpec == nil {
		return fmt.Errorf("depspec cannot be nil if buf is not nil")
	}

	if ccpack.env == nil {
		return fmt.Errorf("env cannot be nil if buf and depspec are not nil")
	}

	if ccpack.data == nil {
		return fmt.Errorf("nil data")
	}

	ccname := ccpack.depSpec.ChaincodeSpec.ChaincodeId.Name
	ccversion := ccpack.depSpec.ChaincodeSpec.ChaincodeId.Version

	//return error if chaincode exists
	path := fmt.Sprintf("%s/%s.%s", chaincodeInstallPath, ccname, ccversion)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("chaincode %s exists", path)
	}

	if err := ioutil.WriteFile(path, ccpack.buf, 0644); err != nil {
		return err
	}

	return nil
}
