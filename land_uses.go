package main

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"
	landusesv1 "github.com/civil-labs/civil-api-go/civil/mesh/landuses/v1"
)

func (s *ParcelServer) GetLandUses(
	ctx context.Context,
	req *connect.Request[landusesv1.GetLandUsesRequest],
) (*connect.Response[landusesv1.GetLandUsesResponse], error) {
	s.logger.Debug("received GetLandUses request")

	rows, err := s.db.Query(ctx, `
		SELECT 
			lu.public_id::text, 
			lu.name, 
			COALESCE(lu.code, ''), 
			COALESCE(lu.description, ''), 
			COALESCE(lut.public_id::text, ''), 
			COALESCE(lut.name, '')
		FROM land_uses lu
		LEFT JOIN land_use_types lut ON lu.land_use_type_id = lut.land_use_type_id
	`)
	if err != nil {
		s.logger.Error("failed to query land uses", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("database query error"))
	}
	defer rows.Close()

	landUses := make(map[string]*landusesv1.LandUse)
	for rows.Next() {
		var publicID, name, code, description, typeID, typeName string
		if err := rows.Scan(&publicID, &name, &code, &description, &typeID, &typeName); err != nil {
			s.logger.Error("failed to scan land use row", slog.Any("error", err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("data scanning error"))
		}
		landUses[publicID] = &landusesv1.LandUse{
			Id:              publicID,
			Name:            name,
			Code:            code,
			Description:     description,
			LandUseTypeId:   typeID,
			LandUseTypeName: typeName,
		}
	}

	if err := rows.Err(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("error reading database rows"))
	}

	return connect.NewResponse(&landusesv1.GetLandUsesResponse{
		LandUses: landUses,
	}), nil
}

func (s *ParcelServer) GetLandUseTypes(
	ctx context.Context,
	req *connect.Request[landusesv1.GetLandUseTypesRequest],
) (*connect.Response[landusesv1.GetLandUseTypesResponse], error) {
	s.logger.Debug("received GetLandUseTypes request")

	rows, err := s.db.Query(ctx, `
		SELECT 
			public_id::text, 
			name, 
			COALESCE(code, ''), 
			COALESCE(description, '') 
		FROM land_use_types
	`)
	if err != nil {
		s.logger.Error("failed to query land use types", slog.Any("error", err))
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("database query error"))
	}
	defer rows.Close()

	types := make(map[string]*landusesv1.LandUseType)
	for rows.Next() {
		var publicID, name, code, description string
		if err := rows.Scan(&publicID, &name, &code, &description); err != nil {
			s.logger.Error("failed to scan land use type row", slog.Any("error", err))
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("data scanning error"))
		}
		types[publicID] = &landusesv1.LandUseType{
			Id:          publicID,
			Name:        name,
			Code:        code,
			Description: description,
		}
	}

	if err := rows.Err(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("error reading database rows"))
	}

	return connect.NewResponse(&landusesv1.GetLandUseTypesResponse{
		LandUseTypes: types,
	}), nil
}
