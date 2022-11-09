package plan

import "github.com/nebuly-ai/nebulnetes/pkg/gpu/mig"

type CreateOperation struct {
	MigProfile mig.Profile
	Quantity   int
}

type DeleteOperation struct {
	// Resources are the possible device resources that can be deleted. Must be >= Quantity.
	Resources mig.DeviceResourceList
	// Quantity is the amount of resources that need to be deleted. Must be <= len(Resources).
	Quantity int
}

func (o DeleteOperation) GetMigProfileName() mig.ProfileName {
	if len(o.Resources) > 0 {
		return o.Resources[0].GetMigProfileName()
	}
	return ""
}

// OperationStatus represents the outcome of the execution of an operation
type OperationStatus struct {
	// PluginRestartRequired indicates if the operation execution requires the NVIDIA device plugin to be restarted
	PluginRestartRequired bool
	// Err corresponds to any error generated by the operation execution
	Err error
}

type CreateOperationList []CreateOperation

func (c CreateOperationList) Flatten() mig.ProfileList {
	res := make(mig.ProfileList, 0)
	for _, op := range c {
		for i := 0; i < op.Quantity; i++ {
			res = append(res, op.MigProfile)
		}
	}
	return res
}

type DeleteOperationList []DeleteOperation