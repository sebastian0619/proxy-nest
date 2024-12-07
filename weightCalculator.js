/**
 * 计算基础权重
 * 基于响应时间计算基础权重，响应时间越短权重越高
 * @param {number} responseTime - 响应时间（毫秒）
 * @param {number} multiplier - 权重乘数
 * @returns {number} 基础权重
 */
function calculateBaseWeight(responseTime, multiplier = 10) {
  // 使用反比例函数：权重 = multiplier * (1000 / responseTime)
  // 1000是基准响应时间（1秒）
  const weight = multiplier * (1000 / responseTime);
  
  // 限制权重范围在 1-100 之间
  return Math.max(1, Math.min(100, weight));
}

/**
 * 计算动态权重
 * 基于EWMA（指数加权移动平均）响应时间计算动态权重
 * @param {number} ewmaResponseTime - EWMA响应时间
 * @param {number} multiplier - 权重乘数
 * @returns {number} 动态权重
 */
function calculateDynamicWeight(ewmaResponseTime, multiplier = 15) {
  // 使用类似的反比例函数，但乘数不同
  const weight = multiplier * (1000 / ewmaResponseTime);
  
  // 限制权重范围在 1-100 之间
  return Math.max(1, Math.min(100, weight));
}

/**
 * 计算综合权重
 * 结合基础权重和动态权重
 * @param {Object} params - 权重参数
 * @param {number} params.baseWeight - 基础权重
 * @param {number} params.dynamicWeight - 动态权重
 * @returns {number} 综合权重
 */
function calculateCombinedWeight({ baseWeight, dynamicWeight }) {
  // 基础权重和动态权重的加权平均
  // 这里我们给予动态权重更高的重要性（60%）
  const combinedWeight = (baseWeight * 0.4 + dynamicWeight * 0.6);
  
  // 限制最终权重范围在 1-100 之间
  return Math.max(1, Math.min(100, combinedWeight));
}

module.exports = {
  calculateBaseWeight,
  calculateDynamicWeight,
  calculateCombinedWeight
}; 