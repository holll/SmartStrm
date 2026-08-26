package organize

import (
	"context"
	"fmt"
	"strings"

	"smartstrm/internal/driver"
)

// Client 整理所需的 OpenList 操作接口（由 driver.OpenList 满足，便于测试注入 mock）
type Client interface {
	driver.Driver
	Mkdir(ctx context.Context, path string) error
	Move(ctx context.Context, srcDir, dstDir string, names []string) error
}

// Options 整理选项
type Options struct {
	TargetPath string // 待整理目录，如 /115/AV-cli
	Mode       string // organize / move / all
	IDMode     string // AV / FC2
	DryRun     bool   // true 仅预览计划
	Overwrite  bool   // 目标同名存在时删除后覆盖
}

// MoveOp 一次移动/重命名计划项
type MoveOp struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// Result 整理结果
type Result struct {
	Plan   []MoveOp `json:"plan"`
	Errors []string `json:"errors"`
}

// Organizer 一次整理的运行时状态（缓存目录/名称集合，减少重复 fs_list）
type Organizer struct {
	client         Client
	opts           Options
	existingDirs   map[string]bool
	existingNames  map[string]bool
	targetDirCache map[string]map[string]bool
	errs           []string
}

// Run 执行整理，按 Mode 分派；返回计划与错误
func Run(ctx context.Context, client Client, o Options) (*Result, error) {
	org := &Organizer{
		client:         client,
		opts:           o,
		existingDirs:   map[string]bool{},
		existingNames:  map[string]bool{},
		targetDirCache: map[string]map[string]bool{},
	}
	res := &Result{}

	switch strings.ToLower(o.Mode) {
	case "organize":
		items, err := org.client.List(ctx, o.TargetPath)
		if err != nil {
			return nil, err
		}
		res.Plan = org.organizeInside(ctx, items)
	case "move":
		if id := strings.ToUpper(o.IDMode); id != "AV" && id != "FC2" {
			return nil, fmt.Errorf("move 模式需要指定番号模式 AV 或 FC2")
		}
		items, err := org.client.List(ctx, o.TargetPath)
		if err != nil {
			return nil, err
		}
		res.Plan = org.moveToLibrary(ctx, items)
	case "all":
		items, err := org.client.List(ctx, o.TargetPath)
		if err != nil {
			return nil, err
		}
		// organize 已直接入库（AV→{库}/{首字母}/{番号}，FC2→{库}/{番号}），
		// 预览只展示最终路径；执行后再把 TargetPath 下遗留的标准番号文件夹一并移入（兼容已有整理产物）
		res.Plan = org.organizeInside(ctx, items)
		if !o.DryRun {
			items2, err := org.client.List(ctx, o.TargetPath)
			if err == nil {
				res.Plan = append(res.Plan, org.moveToLibrary(ctx, items2)...)
			}
		}
	default:
		return nil, fmt.Errorf("未知模式: %s", o.Mode)
	}
	res.Errors = org.errs
	return res, nil
}

// ============ 基础 ============

func (o *Organizer) ensureDir(ctx context.Context, full string) {
	if o.existingDirs[full] {
		return
	}
	if err := o.client.Mkdir(ctx, full); err != nil {
		// 目标已存在视为成功，避免再 list 父目录确认
		if isExistsError(err) {
			o.existingDirs[full] = true
			return
		}
		if !o.pathExists(ctx, full) {
			o.errs = append(o.errs, fmt.Sprintf("创建目录失败 %s: %v", full, err))
			return
		}
	}
	o.existingDirs[full] = true
}

func (o *Organizer) pathExists(ctx context.Context, full string) bool {
	parent, name := driver.SplitPath(full)
	if parent == "" {
		return false
	}
	items, err := o.client.List(ctx, parent)
	if err != nil {
		return false
	}
	for _, it := range items {
		if it.Name == name {
			return true
		}
	}
	return false
}

func (o *Organizer) removeIfExists(ctx context.Context, parent, name string) {
	if !o.existingNames[name] {
		return
	}
	_ = o.client.Remove(ctx, joinPath(parent, name))
	delete(o.existingNames, name)
}

// safeMove 移动条目；overwrite 时目标同名先删除再重试
func (o *Organizer) safeMove(ctx context.Context, srcDir, dstDir, filename string) bool {
	if err := o.client.Move(ctx, srcDir, dstDir, []string{filename}); err == nil {
		return true
	} else if o.opts.Overwrite && isExistsError(err) {
		_ = o.client.Remove(ctx, joinPath(dstDir, filename))
		if err2 := o.client.Move(ctx, srcDir, dstDir, []string{filename}); err2 == nil {
			return true
		}
	}
	return false
}

// isExistsError 判断是否为"目标已存在"类错误
func isExistsError(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "exists")
}

// ============ 命名规则（移植 ops.py） ============

// buildSingleCodeUp 单部代码大写化；AV 单部标签(c/u/uc/cu)拼到代码后
func buildSingleCodeUp(baseCode, label, idMode string) string {
	baseUp := strings.ToUpper(baseCode)
	if strings.ToUpper(idMode) == "AV" && label != "" && singleLabels[strings.ToLower(label)] {
		return baseUp + "-" + strings.ToUpper(label)
	}
	return baseUp
}

// buildTargetName 构造 cli 内部整理用的目录名与文件名
// 单部不保留原文件名后缀内容：目录名与文件名均取规范化的番号（含单部标签）；
// 分集固定保留分集编号
func buildTargetName(ext, baseCode, label string, isSingle bool, idMode string) (folderName, newName string) {
	baseUp := strings.ToUpper(baseCode)
	if !isSingle {
		labelUp := strings.ToUpper(label)
		return baseUp, baseUp + "-" + labelUp + ext
	}
	codeUp := buildSingleCodeUp(baseCode, label, idMode)
	return codeUp, codeUp + ext
}

// isUncensored 判断文件名是否含无码（uncensored）标记
func isUncensored(name string) bool {
	return strings.Contains(strings.ToLower(name), "uncensored")
}

// hasUClassTag 判断单部标签是否已表示无码（U/UC/CU）
func hasUClassTag(label string) bool {
	switch strings.ToLower(label) {
	case "u", "uc", "cu":
		return true
	}
	return false
}

// ============ 模块1：cli 内部整理 ============

func (o *Organizer) organizeInside(ctx context.Context, items []driver.File) []MoveOp {
	// 预收集目录集合与名称集合，重名判断走内存，避免逐文件 fs_list
	for _, it := range items {
		if !it.IsDir {
			o.existingNames[it.Name] = true
			continue
		}
		o.existingDirs[joinPath(o.opts.TargetPath, it.Name)] = true
		o.existingNames[it.Name] = true
	}

	var plan []MoveOp
	for _, it := range items {
		if it.IsDir || !isVideoFile(it.Name) {
			continue
		}
		oldName := it.Name
		oldBase, ext := splitExt(oldName)
		baseCode, label, isSingle, ok := parseVideoID(oldBase, o.opts.IDMode)
		if !ok {
			continue
		}
		// uncensored 视为 -U 单部标签：单部且无 U 类标签时补上
		if isSingle && isUncensored(oldName) && !hasUClassTag(label) {
			label = "u"
		}
		folderName, newName := buildTargetName(ext, baseCode, label, isSingle, o.opts.IDMode)

		oldAbs := joinPath(o.opts.TargetPath, oldName)
		// 一步入库：目标番号文件夹直接落在分类库（AV→{库}/{首字母}/{番号}，FC2→{库}/{番号}）
		cliFolder := joinPath(buildLibraryBasePath(o.opts.TargetPath, baseCode, o.opts.IDMode), folderName)
		cliNewAbs := cliFolder

		if o.opts.DryRun {
			plan = append(plan, MoveOp{Old: oldAbs, New: cliNewAbs})
			continue
		}

		o.ensureDir(ctx, cliFolder)
		if o.renameAndMoveIntoCli(ctx, oldAbs, oldName, newName, cliFolder) {
			plan = append(plan, MoveOp{Old: oldAbs, New: cliNewAbs})
		}
	}
	return plan
}

// renameAndMoveIntoCli 重命名（如需）并移入 cli 番号文件夹；返回是否实际移动
func (o *Organizer) renameAndMoveIntoCli(ctx context.Context, oldAbs, oldName, newName, cliFolder string) bool {
	currentName := oldName

	if newName != oldName {
		renameTarget := joinPath(o.opts.TargetPath, newName)
		if o.existingNames[newName] {
			if !o.opts.Overwrite {
				o.errs = append(o.errs, fmt.Sprintf("目标文件已存在，跳过: %s", renameTarget))
				return false
			}
			o.removeIfExists(ctx, o.opts.TargetPath, newName)
		}
		if err := o.client.Rename(ctx, joinPath(o.opts.TargetPath, oldName), newName); err != nil {
			o.errs = append(o.errs, fmt.Sprintf("重命名失败 %s: %v", oldName, err))
			return false
		}
		delete(o.existingNames, oldName)
		o.existingNames[newName] = true
		currentName = newName
	}

	if o.safeMove(ctx, o.opts.TargetPath, cliFolder, currentName) {
		// 文件已移出，移除名称集合残留，避免后续误判重名
		delete(o.existingNames, currentName)
		return true
	}
	if currentName != oldName {
		// 回滚重命名
		if err := o.client.Rename(ctx, joinPath(o.opts.TargetPath, currentName), oldName); err == nil {
			delete(o.existingNames, currentName)
			o.existingNames[oldName] = true
		} else {
			o.errs = append(o.errs, fmt.Sprintf("回滚重命名失败 %s: %v", currentName, err))
		}
	}
	return false
}

// ============ 模块2：移动到 AV/首字母 分类目录 ============

// buildLibraryBasePath 计算 move 的目标库根路径：
// - 目标目录以 -cli 结尾时先去后缀（/115/AV-cli → /115/AV），库与 cli 目录同层
// - AV：`{库根}/{首字母}`（ABC-123 → /115/AV/A）
// - FC2：直接进 `{库根}`（/115/FC2，FC2 番号无首字母分类）
func buildLibraryBasePath(targetPath, baseCode, idMode string) string {
	stripped := strings.TrimSuffix(targetPath, "/")
	if strings.HasSuffix(strings.ToLower(stripped), "-cli") {
		stripped = stripped[:len(stripped)-4]
	}
	if strings.ToUpper(idMode) == "FC2" {
		return stripped
	}
	first := strings.ToUpper(baseCode)[:1]
	return joinPath(stripped, first)
}

func isStandardAvFolder(name string) (baseCode, label string, isSingle, ok bool) {
	base, label, isSingle, ok := parseAvVideoID(name)
	if !ok || !isSingle {
		return "", "", false, false
	}
	if strings.ToUpper(name) != buildSingleCodeUp(base, label, "AV") {
		return "", "", false, false
	}
	return base, label, true, true
}

// isStandardFolder 按番号模式判断是否为已整理好的标准番号文件夹（move 的移动对象）
func isStandardFolder(name, idMode string) (baseCode, label string, isSingle, ok bool) {
	switch strings.ToUpper(idMode) {
	case "FC2":
		base, label, isSingle, ok := parseFc2VideoID(name)
		if !ok || !isSingle {
			return "", "", false, false
		}
		if strings.ToUpper(name) != strings.ToUpper(base) {
			return "", "", false, false
		}
		return base, label, true, true
	default:
		return isStandardAvFolder(name)
	}
}

func (o *Organizer) moveToLibrary(ctx context.Context, items []driver.File) []MoveOp {
	// 目标分类目录（AV/首字母/）内容缓存：每个首字母目录只 fs_list 一次
	var plan []MoveOp

	for _, it := range items {
		if !it.IsDir {
			continue
		}
		folderName := it.Name
		baseCode, _, _, ok := isStandardFolder(folderName, o.opts.IDMode)
		if !ok {
			continue
		}
		baseTarget := buildLibraryBasePath(o.opts.TargetPath, baseCode, o.opts.IDMode)
		oldAbs := joinPath(o.opts.TargetPath, folderName)
		newAbs := joinPath(baseTarget, folderName)
		if oldAbs == newAbs {
			continue
		}

		if o.opts.DryRun {
			plan = append(plan, MoveOp{Old: oldAbs, New: newAbs})
			continue
		}

		o.ensureDir(ctx, baseTarget)

		if _, ok := o.targetDirCache[baseTarget]; !ok {
			tl, err := o.client.List(ctx, baseTarget)
			if err == nil {
				o.targetDirCache[baseTarget] = map[string]bool{}
				for _, x := range tl {
					o.targetDirCache[baseTarget][x.Name] = true
				}
			}
		}
		names := o.targetDirCache[baseTarget]

		if names[folderName] {
			if !o.opts.Overwrite {
				o.errs = append(o.errs, fmt.Sprintf("目标文件夹已存在，跳过移动: %s", newAbs))
				continue
			}
			// 覆盖模式：删除库内旧文件夹后移入
			_ = o.client.Remove(ctx, newAbs)
			delete(names, folderName)
		}

		if o.safeMove(ctx, o.opts.TargetPath, baseTarget, folderName) {
			names[folderName] = true
			plan = append(plan, MoveOp{Old: oldAbs, New: newAbs})
		}
	}
	return plan
}

func joinPath(parent, name string) string {
	if parent == "/" {
		return "/" + name
	}
	return strings.TrimSuffix(parent, "/") + "/" + name
}
