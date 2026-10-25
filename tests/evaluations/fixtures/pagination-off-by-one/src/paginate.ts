export function paginate<T>(items: T[], page: number, size: number): T[] {
  const start = (page - 1) * size;
  const end = start + size - 1;
  return items.slice(start, end);
}