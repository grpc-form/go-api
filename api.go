package api

import (
	"github.com/grpc-form/api/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	"net"
	"regexp"
	"strconv"
	"sync"
	"context"
)

type ModelFunc func() *grpcform.Form

type SendFunc func(context.Context, *grpcform.Form) (*grpcform.SendFormResponse, error)

func New() ProxyServer {
	return make(map[string]element)
}

func (s ProxyServer) Add(model ModelFunc, send SendFunc) {
	safe.Lock()
	s[model().GetName()] = element{model: model, send: send}
	safe.Unlock()
}

var (
	safe sync.Mutex
)

type ProxyServer map[string]element

type element struct {
	model ModelFunc
	send  SendFunc
}

func (s ProxyServer) Start(host string) error {
	lis, err := net.Listen("tcp", host)
	if err != nil {
		return err
	}
	gs := grpc.NewServer()
	grpcform.RegisterFormServiceServer(gs, s)
	reflection.Register(gs)
	if err := gs.Serve(lis); err != nil {
		return err
	}
	return nil
}

func (s ProxyServer) GetForm(ctx context.Context, req *grpcform.GetFormRequest) (
	*grpcform.Form,
	error,
) {
	safe.Lock()
	defer safe.Unlock()
	for _, e := range s {
		if req.GetName() == e.model().GetName() {
			return e.model(), nil
		}
	}
	return &grpcform.Form{}, nil
}

func (s ProxyServer) ValidateForm(ctx context.Context, in *grpcform.Form) (
	*grpcform.Form,
	error,
) {
	out, err := s.GetForm(ctx, &grpcform.GetFormRequest{Name: in.GetName()})
	if err != nil || in == nil || len(out.GetFields()) != len(in.GetFields()) {
		return &grpcform.Form{}, nil
	}
	out.Valid = true
	outFields := out.GetFields()
	inFields := in.GetFields()
	for i, inField := range inFields {
		outField := outFields[i]
		if activeIf := outField.GetActiveIf(); activeIf != nil {
			checkValidator(inFields, outField, activeIf.GetValidators(),
				grpcform.FieldStatus_FIELD_STATUS_ACTIVE)
		}
		if requiredIf := outField.GetRequiredIf(); requiredIf != nil {
			checkValidator(inFields, outField, requiredIf.GetValidators(),
				grpcform.FieldStatus_FIELD_STATUS_REQUIRED)
		}
		if disabledIf := outField.GetDisabledIf(); disabledIf != nil {
			checkValidator(inFields, outField, disabledIf.GetValidators(),
				grpcform.FieldStatus_FIELD_STATUS_DISABLED)
		}
		if hiddenIf := outField.GetHiddenIf(); hiddenIf != nil {
			checkValidator(inFields, outField, hiddenIf.GetValidators(),
				grpcform.FieldStatus_FIELD_STATUS_HIDDEN)
		}
		if inTextField := inField.GetTextField(); hasStatus(outField.GetStatus(),
			grpcform.FieldStatus_FIELD_STATUS_ACTIVE, grpcform.FieldStatus_FIELD_STATUS_REQUIRED) &&
			inTextField != nil {
			outTextField := outField.GetTextField()
			outTextField.Value = inTextField.GetValue()
			if !out.Valid ||
				outField.GetStatus() == grpcform.FieldStatus_FIELD_STATUS_UNSPECIFIED ||
				(outField.GetStatus() == grpcform.FieldStatus_FIELD_STATUS_ACTIVE &&
					outTextField.Value == "") {
				continue
			}
			if int64(len(outTextField.GetValue())) < outTextField.GetMin() {
				outField.Error = outTextField.GetMinError()
				out.Valid = false
				continue
			}
			if int64(len(outTextField.GetValue())) > outTextField.GetMax() {
				outField.Error = outTextField.GetMaxError()
				out.Valid = false
				continue
			}
			if ok, err := regexp.MatchString(outTextField.GetRegex(),
				outTextField.GetValue()); !ok || err != nil {
				outField.Error = outTextField.GetRegexError()
				out.Valid = false
				continue
			}
		}
		if inSelectField := inField.GetSelectField(); hasStatus(outField.GetStatus(),
			grpcform.FieldStatus_FIELD_STATUS_ACTIVE, grpcform.FieldStatus_FIELD_STATUS_REQUIRED) &&
			inSelectField != nil {
			outSelectField := outField.GetSelectField()
			outSelectField.Index = inSelectField.GetIndex()
			if !out.Valid || (outField.GetStatus() == grpcform.FieldStatus_FIELD_STATUS_ACTIVE &&
				outSelectField.GetIndex() == 0) {
				continue
			}
			check := false
			for _, o := range outSelectField.GetOptions() {
				if o.GetIndex() == outSelectField.GetIndex() {
					check = true
					continue
				}
			}
			if !check {
				outField.Error = outSelectField.GetError()
				out.Valid = false
				continue
			}
		}
		if inNumericField := inField.GetNumericField(); hasStatus(outField.GetStatus(),
			grpcform.FieldStatus_FIELD_STATUS_ACTIVE, grpcform.FieldStatus_FIELD_STATUS_REQUIRED) &&
			inNumericField != nil {
			outSlider := outField.GetNumericField()
			outSlider.Value = inNumericField.GetValue()
			if !out.Valid || (outField.GetStatus() == grpcform.FieldStatus_FIELD_STATUS_ACTIVE &&
				outSlider.Value == 0) {
				continue
			}
			v := outSlider.GetValue()
			if int64(v) < outSlider.GetMin() {
				outField.Error = outSlider.GetMinError()
				out.Valid = false
				continue
			}
			if int64(v) > outSlider.GetMax() {
				outField.Error = outSlider.GetMaxError()
				out.Valid = false
				continue
			}
		}
	}
	if out.GetValid() {
		for _, b := range out.GetButtons() {
			b.Status = grpcform.ButtonStatus_BUTTON_ACTIVE
		}
	}
	return out, nil
}

func (s ProxyServer) SendForm(ctx context.Context, in *grpcform.Form) (
	res *grpcform.SendFormResponse,
	err error,
) {
	out, err := s.ValidateForm(ctx, in)
	if err != nil {
		return &grpcform.SendFormResponse{Form: out}, nil
	}
	return s[out.GetName()].send(ctx, out)
}

func checkValidator(inFields []*grpcform.Field, outField *grpcform.Field, validators []*grpcform.Validator, status grpcform.FieldStatus) {
	for _, validator := range validators {
		index := validator.GetIndex()
		if textField := inFields[index].GetTextField(); textField != nil &&
			checkValidatorOnTextField(textField, validator) {
			outField.Status = status
			break
		}

		if numericField := inFields[index].GetNumericField(); numericField != nil &&
			checkValidatorOnNumericField(numericField, validator) {
			outField.Status = status
			break
		}
		if selectField := inFields[index].GetSelectField(); selectField != nil &&
			checkValidatorOnSelectField(selectField, validator) {
			outField.Status = status
			break
		}
	}
}

func checkValidatorOnTextField(
	textField *grpcform.TextField,
	validator *grpcform.Validator,
) bool {
	if v := validator.GetTextIsEqual(); v != "" &&
		textField.GetValue() == v {
		return true
	}
	if v := validator.GetLengthSmallerThan(); v != 0 &&
		int64(len(textField.GetValue())) < v {
		return true
	}
	if v := validator.GetLengthGreaterThan(); v != 0 &&
		int64(len(textField.GetValue())) > v {
		return true
	}
	if v := validator.GetMatchRegexPattern(); v != "" {
		if ok, err := regexp.MatchString(v, textField.GetValue()); ok &&
			err != nil {
			return true
		}
	}
	return false
}

func checkValidatorOnNumericField(numericField *grpcform.NumericField, validator *grpcform.Validator) bool {
	if v := validator.GetNumberIsEqual(); v != 0 && numericField.GetValue() == v {
		return true
	}
	if v := validator.GetNumberSmallerThan(); v != 0 && numericField.GetValue() < v {
		return true
	}
	if v := validator.GetNumberGreaterThan(); v != 0 && numericField.GetValue() > v {
		return true
	}
	if v := validator.GetMatchRegexPattern(); v != "" {
		if ok, err := regexp.MatchString(v, strconv.Itoa(int(numericField.GetValue()))); ok && err != nil {
			return true
		}
	}
	return false
}

func checkValidatorOnSelectField(selectField *grpcform.SelectField, validator *grpcform.Validator) bool {
	if text := validator.GetTextIsEqual(); text != "" {
		if getOption(selectField.GetIndex(), selectField.GetOptions()) != nil {
			return true
		}
	}
	if v := validator.GetNumberIsEqual(); v != 0 && selectField.GetIndex() == v {
		return true
	}
	if v := validator.GetNumberSmallerThan(); v != 0 && selectField.GetIndex() < v {
		return true
	}
	if v := validator.GetNumberGreaterThan(); v != 0 && selectField.GetIndex() > v {
		return true
	}
	if regex := validator.GetMatchRegexPattern(); regex != "" {
		if ok, err := regexp.MatchString(regex, getOption(selectField.GetIndex(),
			selectField.GetOptions()).GetValue()); ok && err != nil {
			return true
		}
	}
	return false
}

func getOption(option int64, options []*grpcform.Option) *grpcform.Option {
	for _, o := range options {
		if o.GetIndex() == option {
			return o
		}
	}
	return nil
}

func hasStatus(is grpcform.FieldStatus, within ...grpcform.FieldStatus) bool {
	for _, s := range within {
		if s == is {
			return true
		}
	}
	return false
}
