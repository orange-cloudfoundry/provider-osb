package application

type Application struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec Spec `json:"spec"`
}

type Spec struct {
	BrockerUrl  string            `json:"brokerUrl"`
	Credentials Credentials       `json:"credentials"`
	OrgId       string            `json:"orgId"`
	SpaceId     string            `json:"spaceId"`
	Context     map[string]string `json:"context"`
}

type Credentials struct {
	SecretName     string         `json:"secretName"`
	HardCodedCreds HardCodedCreds `json:"hardcodedCreds"`
}

type HardCodedCreds struct {
	User     string `json:"user"`
	Password string `json:"password"`
}
