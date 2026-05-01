package oauth

import "testing"

func TestVerifyPKCE_RFC7636AppendixB(t *testing.T) {
	// RFC 7636 Appendix B: verifier "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	// challenge "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if !VerifyPKCE(verifier, challenge) {
		t.Errorf("RFC 7636 Appendix B vector failed verification")
	}
}

func TestVerifyPKCE_BadVerifier(t *testing.T) {
	if VerifyPKCE("wrong", "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM") {
		t.Errorf("wrong verifier should not verify")
	}
}

func TestVerifyPKCE_Empty(t *testing.T) {
	if VerifyPKCE("", "x") || VerifyPKCE("x", "") {
		t.Errorf("empty inputs should not verify")
	}
}
