package main

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	improvementsv1 "github.com/civil-labs/civil-api-go/civil/mesh/improvements/v1"
)

func (s *APIServer) GetImprovementTypes(
	ctx context.Context,
	req *connect.Request[improvementsv1.GetImprovementTypesRequest],
) (*connect.Response[improvementsv1.GetImprovementTypesResponse], error) {
	s.logger.Debug("received GetImprovementTypes request")

	rows, err := s.db.Query(ctx, `
		SELECT 
			public_id::text, 
			COALESCE(code, ''), 
			name, 
			COALESCE(description, '') 
		FROM improvement_types
	`)
	if err != nil {
		s.logger.Error("failed to query improvement types", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("database query error"))
	}
	defer rows.Close()

	types := make(map[string]*improvementsv1.ImprovementType)
	for rows.Next() {
		var publicID, code, name, description string
		if err := rows.Scan(&publicID, &code, &name, &description); err != nil {
			s.logger.Error("failed to scan improvement type", slog.Any("error", err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("data scanning error"))
		}
		types[publicID] = &improvementsv1.ImprovementType{
			Id:          publicID,
			Code:        code,
			Name:        name,
			Description: description,
		}
	}

	if err := rows.Err(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("error reading database rows"))
	}

	return connect.NewResponse(&improvementsv1.GetImprovementTypesResponse{
		ImprovementTypes: types,
	}), nil
}

func (s *APIServer) GetImprovementConditions(
	ctx context.Context,
	req *connect.Request[improvementsv1.GetImprovementConditionsRequest],
) (*connect.Response[improvementsv1.GetImprovementConditionsResponse], error) {
	s.logger.Debug("received GetImprovementConditions request")

	rows, err := s.db.Query(ctx, `
		SELECT 
			c.public_id::text, 
			a.name, 
			a.depreciation_modifier::float4 
		FROM improvement_conditions c
		JOIN improvement_condition_attributes a ON c.improvement_condition_id = a.improvement_condition_id
		WHERE NOT c.is_voided
		  AND CURRENT_TIMESTAMP <@ a.legal_valid_range
	`)
	if err != nil {
		s.logger.Error("failed to query improvement conditions", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("database query error"))
	}
	defer rows.Close()

	conditions := make(map[string]*improvementsv1.ImprovementCondition)
	for rows.Next() {
		var publicID, name string
		var depreciationModifier float32
		if err := rows.Scan(&publicID, &name, &depreciationModifier); err != nil {
			s.logger.Error("failed to scan improvement condition", slog.Any("error", err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("data scanning error"))
		}
		conditions[publicID] = &improvementsv1.ImprovementCondition{
			Id:                    publicID,
			Name:                  name,
			DepcreciationModifier: depreciationModifier,
		}
	}

	if err := rows.Err(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("error reading database rows"))
	}

	return connect.NewResponse(&improvementsv1.GetImprovementConditionsResponse{
		ImprovementConditions: conditions,
	}), nil
}
