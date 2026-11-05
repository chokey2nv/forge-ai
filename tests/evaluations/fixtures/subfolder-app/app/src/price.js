// applyDiscount returns price reduced by percent (0-100), rounded to cents.
function applyDiscount(price, percent) {
  // Bug: subtracts the percentage as if it were an absolute amount.
  return price - percent;
}

module.exports = { applyDiscount };
