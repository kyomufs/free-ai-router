package provider

// CredentialScheme is implemented by a provider that decides what the credential
// value looks like.
//
// The key is issued by the shared machinery, so the provider does not know which
// one it is getting; what it does know is how its own service wants it spelled
// and what to send when there is no key at all.
type CredentialScheme interface {
	Credential(key string) string
}

// credentialShaped is implemented by a provider that puts the credential
// somewhere other than the default header.
type credentialShaped interface {
	CredentialHeader() (name, prefix string)
}

// ApplyCredential substitutes the credentials into the assembled request.
//
// It lives here rather than in the gateway because the header name, the prefix and
// the anonymity marker are properties of the provider. Pool probing calls the same
// function, and the two paths must not be able to drift: a probe that authenticates
// differently from a client request would measure a different thing.
func ApplyCredential(p Provider, prepared Prepared, key string) {
	if prepared.Headers == nil {
		return
	}
	value := credentialValue(p, key)
	if value == "" {
		return
	}
	name, prefix := credentialShape(p)
	prepared.Headers.Set(name, prefix+value)
}

// credentialValue asks the provider what to put in the header.
func credentialValue(p Provider, key string) string {
	if cs, ok := p.(CredentialScheme); ok {
		return cs.Credential(key)
	}
	if key == "" {
		return "public"
	}
	return key
}

// credentialShape returns the header name and the value prefix.
//
// A provider that cares about this reports the shape itself; the rest get the
// standard one.
func credentialShape(p Provider) (name, prefix string) {
	if shaped, ok := p.(credentialShaped); ok {
		return shaped.CredentialHeader()
	}
	return "authorization", "Bearer "
}
