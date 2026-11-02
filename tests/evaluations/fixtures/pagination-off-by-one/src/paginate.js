function paginate(items, page, pageSize) {
  // Off-by-one bug: page is 1-based, so the slice must start at (page - 1) * pageSize.
  const start = page * pageSize;
  const end = start + pageSize;
  return items.slice(start, end);
}

module.exports = { paginate };
