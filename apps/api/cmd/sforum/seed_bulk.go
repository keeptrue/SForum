package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

// seedBulkDeps 是 bulk 路径需要的最小依赖（用户仍走 Register 保证密码哈希与角色）。
type seedBulkDeps struct {
	pool            *pgxpool.Pool
	identityService identityService
	actors          actorLoader
}

// runPerfBulkSeed 按 perfSeedPlan 流式 bulk 写入。
//
// 设计要点：
//   - 追加生成：新建 perf-cat-* 分类与 seed 用户；不删除已有数据
//   - 不走 forum.Service，避免百万次事务与 domain events
//   - 使用 pgx.CopyFrom 批量写入 posts/topics/comments
//   - 热帖固定 slug，便于 k6 脚本定位
//   - 结束后按真实行数回填 categories.topic_count / comment_count
func runPerfBulkSeed(
	ctx context.Context,
	opts seedOptions,
	plan perfSeedPlan,
	deps seedBulkDeps,
	logf func(format string, args ...any),
) (seedResult, error) {
	start := time.Now()
	result := seedResult{}
	rng := newSeededRand()

	// 1. 确保分类存在。
	categoryIDs, err := ensurePerfCategories(ctx, deps.pool, plan)
	if err != nil {
		return result, err
	}
	logf("categories ready: %d (%s …)", len(categoryIDs), strings.Join(plan.CategorySlugs[:min(3, len(plan.CategorySlugs))], ", "))

	// 2. 注册假用户（数量通常 ≤ 几百，Service 路径可接受）。
	userIDs := make([]int64, 0, plan.Users)
	for i := 0; i < plan.Users; i++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		u, err := generateSeedUser(i)
		if err != nil {
			return result, fmt.Errorf("generate seed user: %w", err)
		}
		current, err := registerSeedUser(ctx, deps.identityService, u)
		if err != nil {
			return result, fmt.Errorf("register seed user: %w", err)
		}
		userIDs = append(userIDs, current.ID)
		result.UsersCreated++
		if result.UsersCreated%50 == 0 || result.UsersCreated == plan.Users {
			logf("  registered %d/%d users", result.UsersCreated, plan.Users)
		}
	}

	// 3. 预渲染正文模板（避免每行跑 Markdown）。
	topicBodies, err := preRenderTopicBodies(rng)
	if err != nil {
		return result, err
	}
	commentBodies, err := preRenderCommentBodies()
	if err != nil {
		return result, err
	}

	// 4. 按批写入主题（含每帖少量普通评论）；热帖正文先写入，评论单独大批量。
	batchSize := opts.Batch
	if batchSize <= 0 {
		batchSize = 5000
	}
	// 热帖 topic id 在写入后记录。
	var hotTopicID int64
	hotWritten := false

	logf("bulk inserting %d topics (batch=%d) …", plan.TopicCount, batchSize)
	for startIdx := 0; startIdx < plan.TopicCount; startIdx += batchSize {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("interrupted after %d topics: %w", result.TopicsCreated, err)
		}
		endIdx := startIdx + batchSize
		if endIdx > plan.TopicCount {
			endIdx = plan.TopicCount
		}
		n := endIdx - startIdx

		// 4a. COPY posts for topic bodies.
		postRows := make([][]any, 0, n)
		meta := make([]topicBatchMeta, 0, n)
		for i := 0; i < n; i++ {
			globalIdx := startIdx + i
			catIdx, _ := plan.categoryForTopicIndex(globalIdx)
			authorID := userIDs[rng.IntN(len(userIDs))]
			body := topicBodies[rng.IntN(len(topicBodies))]
			title := pickString(rng, seedTopicTitles)
			// 唯一 slug：p1m-{globalIdx}；热帖覆盖为固定 slug。
			slug := fmt.Sprintf("p1m-%d", globalIdx)
			if plan.isHotTopic(globalIdx) {
				slug = plan.HotSlug
				title = "【perf】热帖基线：五万评论读路径"
			}
			// 给 raw 加不可见序号后缀，避免 content_hash 全表同一值导致调试困惑（可选）。
			raw := body.RawContent + fmt.Sprintf("\n\n<!--seed:%d-->", globalIdx)
			hash := sha256.Sum256([]byte("markdown\x00" + raw))
			postRows = append(postRows, []any{
				raw,
				body.HTMLContent,
				body.PlainText + fmt.Sprintf(" seed:%d", globalIdx),
				"markdown",
				"markdown",
				"",
				forum.RenderVersion,
				hex.EncodeToString(hash[:]),
				authorID,
				authorID,
			})
			meta = append(meta, topicBatchMeta{
				globalIdx:  globalIdx,
				categoryID: categoryIDs[catIdx],
				authorID:   authorID,
				title:      title,
				slug:       slug,
				isHot:      plan.isHotTopic(globalIdx),
			})
		}

		postIDs, err := copyPosts(ctx, deps.pool, postRows)
		if err != nil {
			return result, fmt.Errorf("copy topic posts [%d,%d): %w", startIdx, endIdx, err)
		}

		// 4b. COPY topics.
		topicRows := make([][]any, 0, n)
		now := time.Now().UTC()
		for i, m := range meta {
			// 轻微打散 last_activity_at，避免全部相同导致排序不稳定。
			activity := now.Add(-time.Duration(plan.TopicCount-m.globalIdx) * time.Second)
			topicRows = append(topicRows, []any{
				m.categoryID,
				m.authorID,
				postIDs[i],
				m.title,
				m.slug,
				"active",
				false,
				int64(0), // comment_count 稍后更新
				int64(0), // view_count
				activity,
				activity,
				activity,
			})
		}
		topicIDs, err := copyTopics(ctx, deps.pool, topicRows)
		if err != nil {
			return result, fmt.Errorf("copy topics [%d,%d): %w", startIdx, endIdx, err)
		}
		result.TopicsCreated += len(topicIDs)

		// 4c. 普通评论（热帖跳过，稍后单独写满 HotComments）。
		for i, m := range meta {
			if m.isHot {
				hotTopicID = topicIDs[i]
				hotWritten = true
				continue
			}
			if plan.RegularCommentsMax <= 0 {
				continue
			}
			cCount := rng.IntN(plan.RegularCommentsMax + 1)
			if cCount == 0 {
				continue
			}
			created, err := bulkInsertFlatComments(ctx, deps.pool, topicIDs[i], userIDs, commentBodies, cCount, rng)
			if err != nil {
				return result, fmt.Errorf("comments on topic %d: %w", topicIDs[i], err)
			}
			result.CommentsCreated += created
		}

		if opts.Batch > 0 && (result.TopicsCreated%opts.Batch == 0 || endIdx == plan.TopicCount) {
			elapsed := time.Since(start)
			rate := float64(result.TopicsCreated) / elapsed.Seconds()
			remaining := plan.TopicCount - result.TopicsCreated
			eta := time.Duration(0)
			if rate > 0 {
				eta = time.Duration(float64(remaining)/rate) * time.Second
			}
			logf("  topics %d/%d (%.0f/s, eta %s, comments %d)",
				result.TopicsCreated, plan.TopicCount, rate, eta.Round(time.Second), result.CommentsCreated)
		}
	}

	// 5. 热帖评论 bulk。
	if plan.HotComments > 0 {
		if !hotWritten || hotTopicID == 0 {
			return result, fmt.Errorf("hot topic was not created (plan index %d)", plan.HotTopicIndex)
		}
		logf("bulk inserting %d hot-thread comments on topic %d (%s) …", plan.HotComments, hotTopicID, plan.HotSlug)
		created, err := bulkInsertHotComments(ctx, deps.pool, hotTopicID, userIDs, commentBodies, plan.HotComments, rng, logf)
		if err != nil {
			return result, fmt.Errorf("hot comments: %w", err)
		}
		result.CommentsCreated += created
	}

	// 6. 回填分类计数（真实 COUNT，仅 seed 结束时跑一次，非公开热路径）。
	if err := refreshCategoryCounters(ctx, deps.pool); err != nil {
		return result, fmt.Errorf("refresh category counters: %w", err)
	}

	// 7. 最终行数校验日志。
	var topicRows, commentRows int64
	if err := deps.pool.QueryRow(ctx, `SELECT COUNT(*) FROM topics`).Scan(&topicRows); err != nil {
		return result, fmt.Errorf("count topics: %w", err)
	}
	if err := deps.pool.QueryRow(ctx, `SELECT COUNT(*) FROM comments`).Scan(&commentRows); err != nil {
		return result, fmt.Errorf("count comments: %w", err)
	}
	logf("db totals after seed: topics=%d comments=%d (this run +%d topics +%d comments)",
		topicRows, commentRows, result.TopicsCreated, result.CommentsCreated)

	result.Elapsed = time.Since(start)
	return result, nil
}

type topicBatchMeta struct {
	globalIdx  int
	categoryID int64
	authorID   int64
	title      string
	slug       string
	isHot      bool
}

type renderedBody struct {
	RawContent  string
	HTMLContent string
	PlainText   string
}

func preRenderTopicBodies(rng *rand.Rand) ([]renderedBody, error) {
	out := make([]renderedBody, 0, 8)
	for i := 0; i < 8; i++ {
		raw := generateTopicBody(rng)
		rendered, err := forum.RenderContent(forum.ContentInput{
			RawContent:   raw,
			SourceFormat: forum.SourceFormatMarkdown,
			EditorType:   forum.EditorTypeMarkdown,
		})
		if err != nil {
			return nil, fmt.Errorf("render topic body template: %w", err)
		}
		out = append(out, renderedBody{
			RawContent:  rendered.RawContent,
			HTMLContent: rendered.HTMLContent,
			PlainText:   rendered.PlainText,
		})
	}
	return out, nil
}

func preRenderCommentBodies() ([]renderedBody, error) {
	out := make([]renderedBody, 0, len(seedCommentBodies))
	for _, raw := range seedCommentBodies {
		rendered, err := forum.RenderContent(forum.ContentInput{
			RawContent:   raw,
			SourceFormat: forum.SourceFormatMarkdown,
			EditorType:   forum.EditorTypeMarkdown,
		})
		if err != nil {
			return nil, fmt.Errorf("render comment body: %w", err)
		}
		out = append(out, renderedBody{
			RawContent:  rendered.RawContent,
			HTMLContent: rendered.HTMLContent,
			PlainText:   rendered.PlainText,
		})
	}
	return out, nil
}

func ensurePerfCategories(ctx context.Context, pool *pgxpool.Pool, plan perfSeedPlan) ([]int64, error) {
	// 取默认分组。
	var groupID int64
	err := pool.QueryRow(ctx, `
		SELECT id FROM category_groups WHERE slug = 'default' LIMIT 1
	`).Scan(&groupID)
	if err != nil {
		return nil, fmt.Errorf("load default category group: %w", err)
	}

	ids := make([]int64, 0, len(plan.CategorySlugs))
	for i, slug := range plan.CategorySlugs {
		name := slug
		if slug == "general" {
			name = "综合讨论"
		} else {
			name = fmt.Sprintf("性能分类 %02d", i)
		}
		var id int64
		// ON CONFLICT 保持 append-only：已存在则只取 id。
		err = pool.QueryRow(ctx, `
			INSERT INTO categories (slug, name, description, visibility, is_system, group_id, position, default_sort)
			VALUES ($1, $2, $3, 'public', FALSE, $4, $5, 'latest')
			ON CONFLICT (slug) DO UPDATE SET slug = EXCLUDED.slug
			RETURNING id
		`, slug, name, "perf seed category (append-only)", groupID, i).Scan(&id)
		if err != nil {
			return nil, fmt.Errorf("ensure category %q: %w", slug, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func copyPosts(ctx context.Context, pool *pgxpool.Pool, rows [][]any) ([]int64, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	// 先 COPY 到临时无 id 表再 INSERT…RETURNING，避免 CopyFrom 拿不到序列 id。
	// 更简单：使用 unnest 批量 INSERT RETURNING。
	raws := make([]string, len(rows))
	htmls := make([]string, len(rows))
	plains := make([]string, len(rows))
	hashes := make([]string, len(rows))
	authors := make([]int64, len(rows))
	for i, r := range rows {
		raws[i] = r[0].(string)
		htmls[i] = r[1].(string)
		plains[i] = r[2].(string)
		hashes[i] = r[7].(string)
		authors[i] = r[8].(int64)
	}
	qrows, err := pool.Query(ctx, `
		INSERT INTO posts (
		  raw_content, html_content, plain_text, source_format,
		  editor_type, editor_version, render_version, content_hash,
		  created_by_user_id, updated_by_user_id
		)
		SELECT
		  r, h, p, 'markdown', 'markdown', '', $4, c, a, a
		FROM unnest($1::text[], $2::text[], $3::text[], $5::text[], $6::bigint[])
		  AS t(r, h, p, c, a)
		RETURNING id
	`, raws, htmls, plains, forum.RenderVersion, hashes, authors)
	if err != nil {
		return nil, err
	}
	defer qrows.Close()
	ids := make([]int64, 0, len(rows))
	for qrows.Next() {
		var id int64
		if err := qrows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := qrows.Err(); err != nil {
		return nil, err
	}
	if len(ids) != len(rows) {
		return nil, fmt.Errorf("post insert returned %d ids, want %d", len(ids), len(rows))
	}
	return ids, nil
}

func copyTopics(ctx context.Context, pool *pgxpool.Pool, rows [][]any) ([]int64, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	catIDs := make([]int64, len(rows))
	authors := make([]int64, len(rows))
	contentIDs := make([]int64, len(rows))
	titles := make([]string, len(rows))
	slugs := make([]string, len(rows))
	activities := make([]time.Time, len(rows))
	for i, r := range rows {
		catIDs[i] = r[0].(int64)
		authors[i] = r[1].(int64)
		contentIDs[i] = r[2].(int64)
		titles[i] = r[3].(string)
		slugs[i] = r[4].(string)
		activities[i] = r[9].(time.Time)
	}
	qrows, err := pool.Query(ctx, `
		INSERT INTO topics (
		  category_id, author_user_id, content_id, title, slug, status,
		  is_pinned, comment_count, view_count, last_activity_at, created_at, updated_at
		)
		SELECT
		  c, a, p, t, s, 'active', FALSE, 0, 0, act, act, act
		FROM unnest(
		  $1::bigint[], $2::bigint[], $3::bigint[], $4::text[], $5::text[], $6::timestamptz[]
		) AS u(c, a, p, t, s, act)
		RETURNING id
	`, catIDs, authors, contentIDs, titles, slugs, activities)
	if err != nil {
		return nil, err
	}
	defer qrows.Close()
	ids := make([]int64, 0, len(rows))
	for qrows.Next() {
		var id int64
		if err := qrows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := qrows.Err(); err != nil {
		return nil, err
	}
	if len(ids) != len(rows) {
		return nil, fmt.Errorf("topic insert returned %d ids, want %d", len(ids), len(rows))
	}
	return ids, nil
}

// bulkInsertFlatComments 为普通主题写入扁平顶层评论（path_key = 自身 id 段）。
func bulkInsertFlatComments(
	ctx context.Context,
	pool *pgxpool.Pool,
	topicID int64,
	userIDs []int64,
	bodies []renderedBody,
	count int,
	rng *rand.Rand,
) (int, error) {
	if count <= 0 {
		return 0, nil
	}
	// 先插 posts，再插 comments（顶层），最后更新 topic.comment_count。
	postRows := make([][]any, 0, count)
	authors := make([]int64, 0, count)
	for i := 0; i < count; i++ {
		body := bodies[rng.IntN(len(bodies))]
		authorID := userIDs[rng.IntN(len(userIDs))]
		raw := body.RawContent + fmt.Sprintf("\n<!--c:%d:%d-->", topicID, i)
		hash := sha256.Sum256([]byte("markdown\x00" + raw))
		postRows = append(postRows, []any{
			raw, body.HTMLContent, body.PlainText, "markdown", "markdown", "", forum.RenderVersion,
			hex.EncodeToString(hash[:]), authorID, authorID,
		})
		authors = append(authors, authorID)
	}
	postIDs, err := copyPosts(ctx, pool, postRows)
	if err != nil {
		return 0, err
	}

	// 预取 comments id 序列，便于写 path_key。
	commentIDs, err := reserveIDs(ctx, pool, "comments_id_seq", count)
	if err != nil {
		return 0, err
	}
	pathKeys := make([]string, count)
	for i, id := range commentIDs {
		pathKeys[i] = formatSeedPathSegment(id)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO comments (
		  id, topic_id, content_id, author_user_id, parent_comment_id, root_comment_id,
		  path_key, depth, reply_count, status
		)
		SELECT
		  id, $1, content_id, author_id, NULL, id, path_key, 0, 0, 'active'
		FROM unnest($2::bigint[], $3::bigint[], $4::bigint[], $5::text[])
		  AS u(id, content_id, author_id, path_key)
	`, topicID, commentIDs, postIDs, authors, pathKeys)
	if err != nil {
		return 0, fmt.Errorf("insert comments: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE topics
		SET comment_count = comment_count + $2,
		    hot_score = (comment_count + $2) * 5 + view_count,
		    last_activity_at = now(),
		    updated_at = now()
		WHERE id = $1
	`, topicID, count); err != nil {
		return 0, err
	}
	return count, nil
}

// bulkInsertHotComments 写入热帖评论：约 1000 根，其余为根下一级回复，形成可测 tree 视图。
func bulkInsertHotComments(
	ctx context.Context,
	pool *pgxpool.Pool,
	topicID int64,
	userIDs []int64,
	bodies []renderedBody,
	total int,
	rng *rand.Rand,
	logf func(format string, args ...any),
) (int, error) {
	if total <= 0 {
		return 0, nil
	}
	// 根评论数量：约 total/50，夹在 [1, total]；与 D2 默认 50 子孙 cap 对齐，便于测 hasMoreChildren。
	rootCount := total / 50
	if rootCount < 1 {
		rootCount = 1
	}
	if rootCount > total {
		rootCount = total
	}
	childCount := total - rootCount

	batch := 2000
	created := 0

	// 根评论。
	rootIDs := make([]int64, 0, rootCount)
	for start := 0; start < rootCount; start += batch {
		n := batch
		if start+n > rootCount {
			n = rootCount - start
		}
		ids, err := insertCommentBatch(ctx, pool, topicID, userIDs, bodies, n, nil, rng)
		if err != nil {
			return created, err
		}
		rootIDs = append(rootIDs, ids...)
		created += n
		if created%10000 == 0 || start+n == rootCount {
			logf("  hot comments %d/%d (roots)", created, total)
		}
	}

	// 子评论：均匀挂到各 root 下（depth=1）。
	for start := 0; start < childCount; start += batch {
		n := batch
		if start+n > childCount {
			n = childCount - start
		}
		parents := make([]int64, n)
		for i := 0; i < n; i++ {
			parents[i] = rootIDs[rng.IntN(len(rootIDs))]
		}
		if _, err := insertCommentBatch(ctx, pool, topicID, userIDs, bodies, n, parents, rng); err != nil {
			return created, err
		}
		created += n
		if created%10000 == 0 || start+n == childCount {
			logf("  hot comments %d/%d", created, total)
		}
	}

	// 回填 root reply_count 与 topic.comment_count。
	if _, err := pool.Exec(ctx, `
		UPDATE comments AS c
		SET reply_count = sub.cnt, updated_at = now()
		FROM (
		  SELECT parent_comment_id AS pid, COUNT(*)::bigint AS cnt
		  FROM comments
		  WHERE topic_id = $1 AND parent_comment_id IS NOT NULL AND status = 'active'
		  GROUP BY parent_comment_id
		) sub
		WHERE c.id = sub.pid
	`, topicID); err != nil {
		return created, fmt.Errorf("update root reply_count: %w", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE topics
		SET comment_count = $2,
		    hot_score = ($2::bigint) * 5 + view_count,
		    last_activity_at = now(),
		    updated_at = now()
		WHERE id = $1
	`, topicID, total); err != nil {
		return created, err
	}
	return created, nil
}

// insertCommentBatch 插入 n 条评论。parents==nil 表示全部顶层；否则 parents[i] 为第 i 条的父评论 id。
func insertCommentBatch(
	ctx context.Context,
	pool *pgxpool.Pool,
	topicID int64,
	userIDs []int64,
	bodies []renderedBody,
	n int,
	parents []int64,
	rng *rand.Rand,
) ([]int64, error) {
	postRows := make([][]any, 0, n)
	authors := make([]int64, 0, n)
	for i := 0; i < n; i++ {
		body := bodies[rng.IntN(len(bodies))]
		authorID := userIDs[rng.IntN(len(userIDs))]
		raw := body.RawContent + fmt.Sprintf("\n<!--h:%d:%d-->", topicID, rng.Int64())
		hash := sha256.Sum256([]byte("markdown\x00" + raw))
		postRows = append(postRows, []any{
			raw, body.HTMLContent, body.PlainText, "markdown", "markdown", "", forum.RenderVersion,
			hex.EncodeToString(hash[:]), authorID, authorID,
		})
		authors = append(authors, authorID)
	}
	postIDs, err := copyPosts(ctx, pool, postRows)
	if err != nil {
		return nil, err
	}
	commentIDs, err := reserveIDs(ctx, pool, "comments_id_seq", n)
	if err != nil {
		return nil, err
	}

	parentIDs := make([]any, n)
	rootIDs := make([]int64, n)
	pathKeys := make([]string, n)
	depths := make([]int32, n)

	// 若有 parent，需要 parent 的 path_key / root_comment_id。
	parentPath := map[int64]struct {
		path string
		root int64
	}{}
	if parents != nil {
		uniq := make([]int64, 0, n)
		seen := map[int64]bool{}
		for _, p := range parents {
			if !seen[p] {
				seen[p] = true
				uniq = append(uniq, p)
			}
		}
		prows, err := pool.Query(ctx, `
			SELECT id, path_key, COALESCE(root_comment_id, id)
			FROM comments WHERE id = ANY($1)
		`, uniq)
		if err != nil {
			return nil, err
		}
		for prows.Next() {
			var id, root int64
			var path string
			if err := prows.Scan(&id, &path, &root); err != nil {
				prows.Close()
				return nil, err
			}
			parentPath[id] = struct {
				path string
				root int64
			}{path: path, root: root}
		}
		prows.Close()
		if err := prows.Err(); err != nil {
			return nil, err
		}
	}

	for i, id := range commentIDs {
		seg := formatSeedPathSegment(id)
		if parents == nil {
			parentIDs[i] = nil
			rootIDs[i] = id
			pathKeys[i] = seg
			depths[i] = 0
			continue
		}
		p := parents[i]
		info := parentPath[p]
		parentIDs[i] = p
		rootIDs[i] = info.root
		pathKeys[i] = info.path + "." + seg
		depths[i] = 1
	}

	// parent_comment_id 用 bigint[] 时 NULL 需特殊处理：用 0 表示无父，插入时 NULLIF。
	parentNums := make([]int64, n)
	for i, p := range parentIDs {
		if p == nil {
			parentNums[i] = 0
		} else {
			parentNums[i] = p.(int64)
		}
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO comments (
		  id, topic_id, content_id, author_user_id, parent_comment_id, root_comment_id,
		  path_key, depth, reply_count, status
		)
		SELECT
		  id, $1, content_id, author_id,
		  NULLIF(parent_id, 0), root_id, path_key, depth, 0, 'active'
		FROM unnest(
		  $2::bigint[], $3::bigint[], $4::bigint[], $5::bigint[], $6::bigint[], $7::text[], $8::int[]
		) AS u(id, content_id, author_id, parent_id, root_id, path_key, depth)
	`, topicID, commentIDs, postIDs, authors, parentNums, rootIDs, pathKeys, depths)
	if err != nil {
		return nil, fmt.Errorf("insert comment batch: %w", err)
	}
	return commentIDs, nil
}

func reserveIDs(ctx context.Context, pool *pgxpool.Pool, seq string, n int) ([]int64, error) {
	// 仅允许已知序列名，避免拼接注入；path_key 需在插入前知道 id。
	switch seq {
	case "comments_id_seq", "posts_id_seq", "topics_id_seq":
	default:
		return nil, fmt.Errorf("refuse unknown sequence %q", seq)
	}
	rows, err := pool.Query(ctx, fmt.Sprintf(
		`SELECT nextval('%s') FROM generate_series(1, $1)`, seq,
	), n)
	if err != nil {
		return nil, fmt.Errorf("reserve ids from %s: %w", seq, err)
	}
	defer rows.Close()
	ids := make([]int64, 0, n)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) != n {
		return nil, fmt.Errorf("reserved %d ids from %s, want %d", len(ids), seq, n)
	}
	return ids, nil
}

func formatSeedPathSegment(id int64) string {
	return fmt.Sprintf("%012d", id)
}

func refreshCategoryCounters(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `
		UPDATE categories c
		SET topic_count = COALESCE(t.cnt, 0),
		    comment_count = COALESCE(t.ccnt, 0),
		    updated_at = now()
		FROM (
		  SELECT category_id,
		         COUNT(*) FILTER (WHERE status IN ('active', 'locked'))::bigint AS cnt,
		         COALESCE(SUM(comment_count) FILTER (WHERE status IN ('active', 'locked')), 0)::bigint AS ccnt
		  FROM topics
		  GROUP BY category_id
		) t
		WHERE c.id = t.category_id
	`)
	return err
}
