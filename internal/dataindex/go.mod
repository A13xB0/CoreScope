module github.com/meshcore-analyzer/dataindex

go 1.22

require github.com/meshcore-analyzer/origin v0.0.0

require github.com/meshcore-analyzer/areamap v0.0.0 // indirect

replace (
	github.com/meshcore-analyzer/areamap => ../areamap
	github.com/meshcore-analyzer/origin => ../origin
)
