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
	pb "github.com/hyperledger/fabric/protos/peer"
)

//----- CDSData ------

//CDSData is data stored in the LCCC on instantiation of a CC
//for CDSPackage.  This needs to be serialized for ChaincodeData
//hence the protobuf format
type CDSData struct {
	//CodeHash hash of CodePackage from ChaincodeDeploymentSpec
	CodeHash []byte `protobuf:"bytes,1,opt,name=codehash,proto3"`

	//MetaDataHash hash of Name and Version from ChaincodeDeploymentSpec
	MetaDataHash []byte `protobuf:"bytes,2,opt,name=metadatahash,proto3"`
}

//----implement functions needed from proto.Message for proto's mar/unmarshal functions

//Reset resets
func (data *CDSData) Reset() { *data = CDSData{} }

//String convers to string
func (data *CDSData) String() string { return proto.CompactTextString(data) }

//ProtoMessage just exists to make proto happy
func (*CDSData) ProtoMessage() {}

//Equals data equals other
func (data *CDSData) Equals(other *CDSData) bool {
	return other != nil && bytes.Equal(data.CodeHash, other.CodeHash) && bytes.Equal(data.MetaDataHash, other.MetaDataHash)
}

//--------- CDSPackage ------------

//CDSPackage encapsulates ChaincodeDeploymentSpec.
type CDSPackage struct {
	buf     []byte
	depSpec *pb.ChaincodeDeploymentSpec
	data    *CDSData
}

// GetDepSpec gets the ChaincodeDeploymentSpec from the package
func (ccpack *CDSPackage) GetDepSpec() *pb.ChaincodeDeploymentSpec {
	return ccpack.depSpec
}

// GetPackageObject gets the ChaincodeDeploymentSpec as proto.Message
func (ccpack *CDSPackage) GetPackageObject() proto.Message {
	return ccpack.depSpec
}

func (ccpack *CDSPackage) getCDSData(cds *pb.ChaincodeDeploymentSpec) ([]byte, *CDSData, error) {
	// check for nil argument. It is an assertion that getCDSData
	// is never called on a package that did not go through/succeed
	// package initialization.
	if cds == nil {
		panic("nil cds")
	}

	b, err := proto.Marshal(cds)
	if err != nil {
		return nil, nil, err
	}

	if err = factory.InitFactories(nil); err != nil {
		return nil, nil, fmt.Errorf("Internal error, BCCSP could not be initialized : %s", err)
	}

	//compute hashes now
	hash, err := factory.GetDefault().GetHash(&bccsp.SHAOpts{})
	if err != nil {
		return nil, nil, err
	}

	cdsdata := &CDSData{}

	//code hash
	cdsdata.CodeHash = hash.Sum(cds.CodePackage)

	hash.Reset()

	//metadata hash
	hash.Write([]byte(cds.ChaincodeSpec.ChaincodeId.Name))
	hash.Write([]byte(cds.ChaincodeSpec.ChaincodeId.Version))

	cdsdata.MetaDataHash = hash.Sum(nil)

	b, err = proto.Marshal(cdsdata)
	if err != nil {
		return nil, nil, err
	}

	return b, cdsdata, nil
}

// ValidateCC returns error if the chaincode is not found or if its not a
// ChaincodeDeploymentSpec
func (ccpack *CDSPackage) ValidateCC(ccdata *ChaincodeData) (*pb.ChaincodeDeploymentSpec, error) {
	if ccpack.depSpec == nil {
		return nil, fmt.Errorf("uninitialized package")
	}

	if ccpack.data == nil {
		return nil, fmt.Errorf("nil data")
	}

	if ccdata.Name != ccpack.depSpec.ChaincodeSpec.ChaincodeId.Name || ccdata.Version != ccpack.depSpec.ChaincodeSpec.ChaincodeId.Version {
		return nil, fmt.Errorf("invalid chaincode data %v (%v)", ccdata, ccpack.depSpec.ChaincodeSpec.ChaincodeId)
	}

	otherdata := &CDSData{}
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
func (ccpack *CDSPackage) InitFromBuffer(buf []byte) (*ChaincodeData, error) {
	//incase ccpack is reused
	ccpack.buf = nil
	ccpack.depSpec = nil
	ccpack.data = nil

	depSpec := &pb.ChaincodeDeploymentSpec{}
	err := proto.Unmarshal(buf, depSpec)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal deployment spec from bytes")
	}

	databytes, data, err := ccpack.getCDSData(depSpec)
	if err != nil {
		return nil, err
	}

	ccpack.buf = buf
	ccpack.depSpec = depSpec
	ccpack.data = data

	return &ChaincodeData{Name: depSpec.ChaincodeSpec.ChaincodeId.Name, Version: depSpec.ChaincodeSpec.ChaincodeId.Version, Data: databytes}, nil
}

//InitFromFS returns the chaincode and its package from the file system
func (ccpack *CDSPackage) InitFromFS(ccname string, ccversion string) ([]byte, *pb.ChaincodeDeploymentSpec, error) {
	//incase ccpack is reused
	ccpack.buf = nil
	ccpack.depSpec = nil
	ccpack.data = nil

	buf, err := GetChaincodePackage(ccname, ccversion)
	if err != nil {
		return nil, nil, err
	}

	depSpec := &pb.ChaincodeDeploymentSpec{}
	err = proto.Unmarshal(buf, depSpec)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal fs deployment spec for %s, %s", ccname, ccversion)
	}

	_, data, err := ccpack.getCDSData(depSpec)
	if err != nil {
		return nil, nil, err
	}

	ccpack.buf = buf
	ccpack.depSpec = depSpec
	ccpack.data = data

	return buf, depSpec, nil
}

//PutChaincodeToFS - serializes chaincode to a package on the file system
func (ccpack *CDSPackage) PutChaincodeToFS() error {
	if ccpack.buf == nil {
		return fmt.Errorf("uninitialized package")
	}

	if ccpack.depSpec == nil {
		return fmt.Errorf("depspec cannot be nil if buf is not nil")
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
